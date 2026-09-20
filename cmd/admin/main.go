package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/Alfonsxh/codex-cpa-pool/internal/accountlifecycle"
	"github.com/Alfonsxh/codex-cpa-pool/internal/accountprojection"
	"github.com/Alfonsxh/codex-cpa-pool/internal/accountstatus"
	adminapi "github.com/Alfonsxh/codex-cpa-pool/internal/admin"
	"github.com/Alfonsxh/codex-cpa-pool/internal/controlplane"
	"github.com/Alfonsxh/codex-cpa-pool/internal/failover"
	"github.com/Alfonsxh/codex-cpa-pool/internal/identity"
	"github.com/Alfonsxh/codex-cpa-pool/internal/notifications"
	"github.com/Alfonsxh/codex-cpa-pool/internal/ownership"
	portalapi "github.com/Alfonsxh/codex-cpa-pool/internal/portal"
	quotaapi "github.com/Alfonsxh/codex-cpa-pool/internal/quota"
	"github.com/Alfonsxh/codex-cpa-pool/internal/runtimeops"
	usagestore "github.com/Alfonsxh/codex-cpa-pool/internal/usage"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

const envPrefix = "CLIPROXY_ADMIN"

type appConfig struct {
	Address               string
	Root                  string
	LogLevel              string
	SessionTTL            time.Duration
	SessionAbsoluteTTL    time.Duration
	PortalSessionTTL      time.Duration
	SecureCookies         bool
	ShutdownTimeout       time.Duration
	GatewayProbes         []string
	SnapshotTimeout       time.Duration
	AccountDrainTimeout   time.Duration
	RuntimeOwner          string
	LeaseTTL              time.Duration
	ComposeProject        string
	ControlComposeProject string
	DockerNetwork         string
	InstanceName          string
	RuntimeReadOnly       bool
}

func main() {
	if err := newCommand().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func newCommand() *cobra.Command {
	settings := viper.New()
	settings.SetEnvPrefix(envPrefix)
	settings.SetEnvKeyReplacer(strings.NewReplacer("-", "_", ".", "_"))
	settings.AutomaticEnv()

	command := &cobra.Command{
		Use:           "cpa-admin",
		Short:         "Run the Go CPA control-plane API",
		SilenceUsage:  true,
		SilenceErrors: true,
		PreRunE: func(command *cobra.Command, _ []string) error {
			configFile, err := command.Flags().GetString("config")
			if err != nil {
				return err
			}
			if configFile == "" {
				return nil
			}
			settings.SetConfigFile(configFile)
			if err := settings.ReadInConfig(); err != nil {
				return fmt.Errorf("read Admin config: %w", err)
			}
			return nil
		},
		RunE: func(_ *cobra.Command, _ []string) error {
			return run(appConfig{
				Address:               settings.GetString("address"),
				Root:                  settings.GetString("root"),
				LogLevel:              settings.GetString("log-level"),
				SessionTTL:            settings.GetDuration("session-ttl"),
				SessionAbsoluteTTL:    settings.GetDuration("session-absolute-ttl"),
				PortalSessionTTL:      settings.GetDuration("portal-session-ttl"),
				SecureCookies:         settings.GetBool("secure-cookies"),
				ShutdownTimeout:       settings.GetDuration("shutdown-timeout"),
				GatewayProbes:         settings.GetStringSlice("gateway-probe-url"),
				SnapshotTimeout:       settings.GetDuration("snapshot-timeout"),
				AccountDrainTimeout:   settings.GetDuration("account-drain-timeout"),
				RuntimeOwner:          settings.GetString("runtime-owner"),
				LeaseTTL:              settings.GetDuration("lease-ttl"),
				ComposeProject:        settings.GetString("compose-project"),
				ControlComposeProject: settings.GetString("control-compose-project"),
				DockerNetwork:         settings.GetString("docker-network-name"),
				InstanceName:          settings.GetString("instance-name"),
				RuntimeReadOnly:       settings.GetBool("runtime-read-only"),
			})
		},
	}
	flags := command.Flags()
	flags.String("config", "", "optional YAML, JSON, or TOML configuration file")
	flags.String("address", ":8318", "Admin API listen address")
	flags.String("root", "/opt/codex-cpa-pool", "existing CPA deployment root")
	flags.String("log-level", "info", "Zap log level")
	flags.Duration("session-ttl", 30*time.Minute, "Admin browser inactivity timeout")
	flags.Duration("session-absolute-ttl", 8*time.Hour, "Admin browser absolute session lifetime")
	flags.Duration("portal-session-ttl", 12*time.Hour, "self-service browser session lifetime")
	flags.Bool("secure-cookies", false, "always mark Admin session cookies Secure")
	flags.Duration("shutdown-timeout", 30*time.Second, "maximum graceful shutdown wait")
	flags.StringSlice("gateway-probe-url", []string{"http://edge:8319"}, "Gateway internal URLs used to confirm snapshot activation")
	flags.Duration("snapshot-timeout", 8*time.Second, "maximum auth snapshot activation wait")
	flags.Duration("account-drain-timeout", 10*time.Minute, "maximum wait for in-flight CPA requests before account rebuild or deletion")
	flags.String("runtime-owner", "codex-cpa", "explicitly activated runtime ownership label")
	flags.Duration("lease-ttl", 30*time.Second, "runtime and Admin ownership lease lifetime")
	flags.String("compose-project", "cliproxy-multi", "exact Docker Compose project label used by runtime operations")
	flags.String("control-compose-project", "codex-cpa", "exact control-plane Docker Compose project label used by configuration reloads")
	flags.String("docker-network-name", "cliproxy-backend", "exact Docker network used by business CPA containers")
	flags.String("instance-name", "cliproxy", "container name prefix used by business CPA containers")
	flags.Bool("runtime-read-only", false, "allow Docker status and log reads but disable all runtime mutations")
	if err := settings.BindPFlags(flags); err != nil {
		panic(err)
	}
	if err := settings.BindEnv("root", "CLIPROXY_ADMIN_ROOT", "CLIPROXY_ROOT"); err != nil {
		panic(err)
	}
	if err := settings.BindEnv("gateway-probe-url", "CLIPROXY_GATEWAY_INTERNAL_URL"); err != nil {
		panic(err)
	}
	if err := settings.BindEnv("compose-project", "CLIPROXY_ADMIN_COMPOSE_PROJECT", "COMPOSE_PROJECT_NAME"); err != nil {
		panic(err)
	}
	if err := settings.BindEnv("control-compose-project", "CLIPROXY_ADMIN_CONTROL_COMPOSE_PROJECT"); err != nil {
		panic(err)
	}
	if err := settings.BindEnv("docker-network-name", "CLIPROXY_ADMIN_DOCKER_NETWORK_NAME", "DOCKER_NETWORK_NAME"); err != nil {
		panic(err)
	}
	if err := settings.BindEnv("instance-name", "CLIPROXY_ADMIN_INSTANCE_NAME", "INSTANCE_NAME"); err != nil {
		panic(err)
	}
	return command
}

func run(config appConfig) error {
	if strings.TrimSpace(config.Address) == "" {
		return errors.New("Admin listen address is required")
	}
	if strings.TrimSpace(config.Root) == "" {
		return errors.New("control-plane root is required")
	}
	if config.SessionTTL <= 0 {
		return errors.New("Admin session TTL must be positive")
	}
	if config.SessionAbsoluteTTL < config.SessionTTL {
		return errors.New("Admin session absolute TTL must not be shorter than idle TTL")
	}
	if config.PortalSessionTTL <= 0 || config.PortalSessionTTL > 30*24*time.Hour {
		return errors.New("Portal session TTL must be positive and not exceed 30 days")
	}
	if config.ShutdownTimeout <= 0 {
		return errors.New("Admin shutdown timeout must be positive")
	}
	if config.SnapshotTimeout <= 0 {
		return errors.New("auth snapshot timeout must be positive")
	}
	if config.AccountDrainTimeout <= 0 {
		return errors.New("account drain timeout must be positive")
	}
	if strings.TrimSpace(config.ComposeProject) == "" {
		return errors.New("account Docker Compose project is required")
	}
	if strings.TrimSpace(config.ControlComposeProject) == "" {
		return errors.New("control Docker Compose project is required")
	}
	if strings.TrimSpace(config.DockerNetwork) == "" || strings.TrimSpace(config.InstanceName) == "" {
		return errors.New("Docker network and instance names are required")
	}
	logger, err := newLogger(config.LogLevel)
	if err != nil {
		return err
	}
	defer func() { _ = logger.Sync() }()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	leaseConfig, err := ownership.WorkerConfig(config.RuntimeOwner, "admin", config.LeaseTTL)
	if err != nil {
		return err
	}
	return ownership.RunWithExistingStore(
		ctx,
		config.Root,
		controlplane.Options{},
		leaseConfig,
		func(runContext context.Context, fenceContext context.Context, store *controlplane.Store) error {
			return runOwnedAdmin(runContext, fenceContext, stop, config, logger, store)
		},
	)
}

func runOwnedAdmin(
	runContext context.Context,
	fenceContext context.Context,
	stop context.CancelFunc,
	config appConfig,
	logger *zap.Logger,
	store *controlplane.Store,
) error {
	persistedStateProvider := failover.RuntimeStateProvider{Store: store}
	usageReader, usageError := usagestore.OpenReadOnly(config.Root, nil)
	if usageError != nil {
		logger.Warn(
			"usage reader unavailable; live usage APIs and account rebalance remain disabled",
			zap.Error(usageError),
		)
	}
	if usageReader != nil {
		defer usageReader.Close()
		if settings, settingsError := store.ReadSettings(runContext); settingsError == nil {
			usageReader.SetActiveUserWindow(usagestore.ActiveUserWindowFromSettings(settings))
		} else {
			logger.Warn("active-user window setting unavailable; using default", zap.Error(settingsError))
		}
	}
	portalStore, portalStoreError := usagestore.OpenPortal(config.Root, nil)
	if portalStoreError != nil {
		logger.Warn(
			"portal session store unavailable; self-service routes remain disabled",
			zap.Error(portalStoreError),
		)
	}
	if portalStore != nil {
		defer portalStore.Close()
	}
	var err error
	var fencedPortalStore *usagestore.FencedPortalStore
	if portalStore != nil {
		fencedPortalStore, err = usagestore.NewFencedPortalStore(portalStore, store)
		if err != nil {
			return err
		}
	}
	snapshotPublisher := &failover.AuthSnapshotPublisher{
		Root: config.Root, Store: store, ProbeURLs: config.GatewayProbes,
		WaitTimeout: config.SnapshotTimeout, Fence: store,
	}
	notificationSender, err := notifications.NewFencedSender(
		&notifications.WebhookSender{Store: store},
		store,
	)
	if err != nil {
		return err
	}
	gatewayDrainer, err := runtimeops.NewGatewayDrainer(runtimeops.GatewayDrainerConfig{
		ProbeURLs: config.GatewayProbes, WaitTimeout: config.AccountDrainTimeout,
	})
	if err != nil {
		return err
	}
	identityOperationLock := &sync.Mutex{}
	identityService := &identity.Service{
		Store: store, Snapshots: snapshotPublisher, Lock: identityOperationLock,
	}
	var runtimeManager *runtimeops.Manager
	var controlRuntimeManager *runtimeops.Manager
	var runtimeJobs *runtimeops.JobManager
	openRuntime := runtimeops.Open
	if config.RuntimeReadOnly {
		openRuntime = runtimeops.OpenReadOnly
	}
	runtimeManager, runtimeError := openRuntime(config.ComposeProject, store)
	if runtimeError != nil {
		logger.Warn("Docker runtime operations remain disabled", zap.Error(runtimeError))
	} else {
		defer runtimeManager.Close()
	}
	if config.ControlComposeProject == config.ComposeProject {
		controlRuntimeManager = runtimeManager
	} else {
		controlRuntimeManager, runtimeError = openRuntime(config.ControlComposeProject, store)
		if runtimeError != nil {
			logger.Warn("control Docker runtime operations remain disabled", zap.Error(runtimeError))
		} else {
			defer controlRuntimeManager.Close()
		}
	}
	runtimeObserver, err := accountstatus.New(accountstatus.Config{Root: config.Root, Secrets: store})
	if err != nil {
		return err
	}
	oauthLoader := quotaapi.OAuthLoader{Root: config.Root}
	stateProvider := liveAccountStateProvider{
		Base: persistedStateProvider, Accounts: store, Runtime: runtimeManager,
		OAuth: oauthLoader, Observer: runtimeObserver,
	}
	listStateProvider := stateProvider
	listStateProvider.Observer = displayAccountObserver{runtimeObserver}
	var rebalancer adminapi.AccountRebalancer
	var routeChanger *failover.Service
	if usageReader != nil {
		routeChanger = &failover.Service{
			Routes:    store,
			States:    stateProvider,
			Activity:  usageReader,
			Snapshots: snapshotPublisher,
			Lock:      identityOperationLock,
		}
		rebalancer = routeChanger
	}
	quotaResetter, err := quotaapi.NewResetter(quotaapi.ResetterConfig{
		Root: config.Root, Store: store, Fence: store, Client: quotaapi.Client{},
	})
	if err != nil {
		return err
	}
	var portalServer *portalapi.Server
	var userManager adminapi.UserLifecycleService
	projectionRenderer := &accountprojection.Renderer{Root: config.Root, Store: store}
	if fencedPortalStore != nil {
		userManager, err = adminapi.NewUserManager(adminapi.UserLifecycleConfig{
			Store: store, Credentials: fencedPortalStore,
			Projection: configurationProjectionAdapter{renderer: projectionRenderer},
			Snapshots:  snapshotPublisher,
			Lock:       identityOperationLock,
		})
		if err != nil {
			return err
		}
		portalServer, err = portalapi.New(portalapi.Config{
			Identity: store, Sessions: fencedPortalStore, Usage: usageReader,
			Quotas:      fencedPortalStore,
			PublicUsage: usageReader, Inflight: gatewayDrainer,
			States: stateProvider, Activity: usageReader, Routes: routeChanger,
			ListStates: listStateProvider,
			Keys:       identityService, QuotaStore: store,
			Logger: logger, SessionTTL: config.PortalSessionTTL,
			SecureCookies: config.SecureCookies,
		})
		if err != nil {
			return err
		}
	}
	var accountManager *accountlifecycle.Manager
	var accountRuntime *runtimeops.AccountRuntime
	var accountRuntimeError error
	composeEnvironmentProjector := &adminapi.AccountComposeEnvironmentProjector{Root: config.Root}
	if runtimeManager != nil {
		if !config.RuntimeReadOnly {
			accountRuntime, accountRuntimeError = runtimeops.NewAccountRuntime(
				runtimeManager,
				store,
				runtimeops.AccountRuntimeConfig{
					Root: config.Root, NetworkName: config.DockerNetwork,
					InstanceName: config.InstanceName, ProbeTimeout: config.SnapshotTimeout,
					ImageProjector: composeEnvironmentProjector, Drainer: gatewayDrainer,
				},
			)
			if accountRuntimeError != nil {
				logger.Warn("account lifecycle runtime remains disabled", zap.Error(accountRuntimeError))
			} else {
				accountManager, err = accountlifecycle.New(accountlifecycle.Config{
					Store: store, Files: &accountlifecycle.FileManager{Root: config.Root},
					Projection: projectionRenderer,
					Snapshots:  snapshotPublisher, Runtime: accountRuntime, Lock: identityOperationLock,
					Drainer: gatewayDrainer, States: stateProvider,
				})
				if err != nil {
					return err
				}
				if err := accountManager.Recover(fenceContext); err != nil {
					return fmt.Errorf("recover interrupted account lifecycle operation before Admin startup: %w", err)
				}
				identityOperationLock.Lock()
				_, renderError := projectionRenderer.Render(fenceContext)
				var migrated []string
				if renderError == nil {
					migrated, renderError = accountRuntime.MigrateLegacyConfigMounts(fenceContext)
				}
				identityOperationLock.Unlock()
				if renderError != nil {
					return fmt.Errorf("migrate account config mounts before Admin startup: %w", renderError)
				}
				if len(migrated) > 0 {
					logger.Info("migrated account config mounts", zap.Strings("accounts", migrated))
				}
			}
		}
	}
	if runtimeManager != nil {
		diagnostics, diagnosticsError := runtimeops.NewDiagnostics(runtimeops.DiagnosticsConfig{
			Root: config.Root, Store: store, Projection: projectionRenderer,
			GatewayProbeURLs: config.GatewayProbes, RenderEnabled: !config.RuntimeReadOnly,
		})
		if diagnosticsError != nil {
			return diagnosticsError
		}
		if accountRuntime != nil {
			runtimeJobs, err = runtimeops.NewJobManagerWithDiagnostics(
				runtimeManager, accountRuntime, accountRuntime, diagnostics, identityOperationLock,
			)
		} else {
			runtimeJobs, err = runtimeops.NewJobManagerWithDiagnostics(
				runtimeManager, nil, nil, diagnostics, identityOperationLock,
			)
		}
		if err != nil {
			return err
		}
		defer runtimeJobs.Close()
	}
	var extensions *runtimeops.Extensions
	if accountRuntime != nil && runtimeJobs != nil {
		extensions = runtimeops.NewExtensions(config.Root, store, extensionRuntimeAdapter{manager: runtimeManager, accounts: accountRuntime}, configurationProjectionAdapter{renderer: projectionRenderer})
		runtimeJobs.SetExtensions(extensions)
		extensionContext, stopExtensions := context.WithCancel(fenceContext)
		extensions.Lifetime = extensionContext
		extensionDone := make(chan struct{})
		go func() { defer close(extensionDone); extensions.Run(extensionContext) }()
		defer func() { stopExtensions(); <-extensionDone }()
	}
	configurationApplier := &adminapi.ConfigurationRuntimeApplier{
		GatewaySnapshots:   snapshotPublisher,
		Accounts:           store,
		Projection:         configurationProjectionAdapter{renderer: projectionRenderer},
		AccountEnvironment: composeEnvironmentProjector,
	}
	if runtimeManager != nil && !config.RuntimeReadOnly {
		configurationApplier.Runtime = configurationRuntimeAdapter{manager: runtimeManager}
	}
	if controlRuntimeManager != nil && !config.RuntimeReadOnly {
		configurationApplier.ControlRuntime = configurationRuntimeAdapter{manager: controlRuntimeManager}
	}
	var modelProbe adminapi.AccountModelProbe
	if runtimeManager != nil {
		modelProbe = runtimeops.NewModelProbe(store, runtimeManager, nil)
	}
	adminServer, err := adminapi.New(adminapi.Config{
		Root:                 config.Root,
		Store:                store,
		Accounts:             store,
		AccountStates:        listStateProvider,
		AccountRuntime:       displayAccountObserver{runtimeObserver},
		ModelProbe:           modelProbe,
		Activity:             usageReader,
		OAuth:                oauthLoader,
		Usage:                usageReader,
		TeamIdentities:       fencedPortalStore,
		NotificationSender:   notificationSender,
		Logger:               logger,
		SessionTTL:           config.SessionTTL,
		SessionAbsoluteTTL:   config.SessionAbsoluteTTL,
		SecureCookies:        config.SecureCookies,
		Rebalancer:           rebalancer,
		Portal:               portalServer,
		Users:                userManager,
		Runtime:              runtimeManager,
		Extensions:           extensions,
		Images:               runtimeManager,
		Release:              adminapi.NewGitHubReleaseCatalog(nil),
		RuntimeJobs:          runtimeJobs,
		AccountLifecycle:     accountManager,
		QuotaResetter:        quotaResetter,
		ConfigurationApplier: configurationApplier,
		OperationLock:        identityOperationLock,
	})
	if err != nil {
		return err
	}
	defer adminServer.Close()
	httpServer := &http.Server{
		Addr:              config.Address,
		Handler:           adminServer.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	errorChannel := make(chan error, 1)
	go func() {
		logger.Info("Go Admin listener ready", zap.String("address", httpServer.Addr))
		err := httpServer.ListenAndServe()
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		errorChannel <- err
	}()

	var runError error
	select {
	case <-runContext.Done():
	case runError = <-errorChannel:
		stop()
	}
	if fenceContext.Err() != nil {
		return errors.Join(runError, httpServer.Close())
	}
	shutdownContext, cancel := context.WithTimeout(context.Background(), config.ShutdownTimeout)
	defer cancel()
	shutdownError := httpServer.Shutdown(shutdownContext)
	if shutdownError != nil {
		_ = httpServer.Close()
	}
	return errors.Join(runError, shutdownError)
}

type accountCatalogReader interface {
	ReadAccounts(context.Context) ([]controlplane.Account, error)
}

type runtimeServiceLister interface {
	List(context.Context) ([]runtimeops.Service, error)
}

type oauthAccountLoader interface {
	Load(string) (quotaapi.OAuthRecord, error)
}

type accountRuntimeObserver interface {
	Observe(context.Context, map[string]string) map[string]accountstatus.State
}

// Both read-only account lists share this bounded runtime cache. Route writes use
// the normal observer and never authorize from an expired display snapshot.
type displayAccountObserver struct {
	observer *accountstatus.Observer
}

func (display displayAccountObserver) Observe(ctx context.Context, services map[string]string) map[string]accountstatus.State {
	return display.observer.ObserveForDisplay(ctx, services)
}

// liveAccountStateProvider preserves the v1 ordering for user-visible route
// eligibility: a disabled account, stopped container, or missing OAuth record
// is never widened into selectable merely because quota state is absent or
// stale. The persisted failover snapshot still owns quota/headroom decisions.
type liveAccountStateProvider struct {
	Base     failover.AccountStateProvider
	Accounts accountCatalogReader
	Runtime  runtimeServiceLister
	OAuth    oauthAccountLoader
	Observer accountRuntimeObserver
}

func (provider liveAccountStateProvider) AccountStates(ctx context.Context) (map[string]failover.AccountState, error) {
	if provider.Base == nil || provider.Accounts == nil || provider.OAuth == nil {
		return nil, errors.New("live account state dependencies are incomplete")
	}
	states, err := provider.Base.AccountStates(ctx)
	if err != nil {
		return nil, err
	}
	accounts, err := provider.Accounts.ReadAccounts(ctx)
	if err != nil {
		return nil, err
	}
	running := make(map[string]bool)
	runningAccountServices := make(map[string]string)
	if provider.Runtime != nil {
		services, listError := provider.Runtime.List(ctx)
		if listError == nil {
			for _, service := range services {
				running[service.Service] = service.State == "running"
			}
		}
	}
	for _, account := range accounts {
		service := "cliproxy-" + account.ID
		if running[service] {
			runningAccountServices[account.ID] = service
		}
	}
	runtimeStates := make(map[string]accountstatus.State)
	if provider.Observer != nil && len(runningAccountServices) > 0 {
		runtimeStates = provider.Observer.Observe(ctx, runningAccountServices)
	}
	for _, account := range accounts {
		state := states[account.ID]
		state.Account = account.ID
		if !account.GroupEnabled {
			state.Eligible, state.Exhausted, state.Headroom, state.Reason = false, false, 0, "account_disabled"
		} else if !running["cliproxy-"+account.ID] {
			state.Eligible, state.Exhausted, state.Headroom, state.Reason = false, false, 0, "container_not_running"
		} else if _, loadError := provider.OAuth.Load(account.ID); errors.Is(loadError, quotaapi.ErrOAuthMissing) {
			state.Eligible, state.Exhausted, state.Headroom, state.Reason = false, false, 0, "oauth_missing"
		} else if loadError != nil {
			state.Eligible, state.Exhausted, state.Headroom, state.Reason = false, false, 0, "oauth_missing"
		} else if state.Exhausted || state.Reason == "quota_exhausted" || state.Reason == "upstream_disallowed" {
			state.Eligible, state.Exhausted, state.Headroom, state.Reason = false, true, 0, "quota_exhausted"
		} else if runtimeState, found := runtimeStates[account.ID]; found {
			switch {
			case runtimeState.Exhausted:
				state.Eligible, state.Exhausted, state.Headroom, state.Reason = false, true, 0, accountstatus.ReasonQuotaExhausted
			case runtimeState.DisableEligibility:
				state.Eligible, state.Exhausted, state.Headroom, state.Reason = false, false, 0, runtimeState.Reason
			case runtimeState.Reason != "":
				// Runtime degradation changes the user-visible status without
				// widening or shrinking the official-quota failover capacity.
				state.Reason = runtimeState.Reason
			}
		}
		states[account.ID] = state
	}
	return states, nil
}

type configurationProjectionAdapter struct {
	renderer *accountprojection.Renderer
}

func (adapter configurationProjectionAdapter) VerifyUserKey(ctx context.Context, user string) error {
	if adapter.renderer == nil {
		return errors.New("account projection renderer is unavailable")
	}
	store, ok := adapter.renderer.Store.(accountprojection.UserKeyReadinessStore)
	if !ok {
		return errors.New("account projection cannot verify user credentials")
	}
	return (accountprojection.UserKeyReadiness{Store: store}).VerifyUserKey(ctx, user)
}

func (adapter configurationProjectionAdapter) RefreshAccounts(ctx context.Context) error {
	if adapter.renderer == nil {
		return errors.New("account projection renderer is unavailable")
	}
	_, err := adapter.renderer.Render(ctx)
	return err
}

type configurationRuntimeAdapter struct {
	manager *runtimeops.Manager
}

func (adapter configurationRuntimeAdapter) RestartConfigurationTarget(
	ctx context.Context,
	target string,
) error {
	if adapter.manager == nil {
		return errors.New("configuration runtime manager is unavailable")
	}
	_, err := adapter.manager.Restart(ctx, target)
	return err
}

func newLogger(level string) (*zap.Logger, error) {
	var parsedLevel zapcore.Level
	if err := parsedLevel.UnmarshalText([]byte(level)); err != nil {
		return nil, fmt.Errorf("invalid log level %q: %w", level, err)
	}
	config := zap.NewProductionConfig()
	config.Level = zap.NewAtomicLevelAt(parsedLevel)
	return config.Build()
}

// The runtime remains scoped to this Admin Compose project.
type extensionRuntimeAdapter struct {
	manager  *runtimeops.Manager
	accounts *runtimeops.AccountRuntime
}

func (a extensionRuntimeAdapter) List(ctx context.Context) ([]runtimeops.Service, error) {
	return a.manager.List(ctx)
}
func (a extensionRuntimeAdapter) RestartRunningAccount(ctx context.Context, id string) error {
	return a.accounts.RestartRunningAccount(ctx, id)
}

func (a extensionRuntimeAdapter) ValidatePlugin(ctx context.Context, id, version string) error {
	return a.accounts.ValidatePlugin(ctx, id, version)
}
