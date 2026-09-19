package failover

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Alfonsxh/codex-cpa-pool/internal/controlplane"
	"github.com/Alfonsxh/codex-cpa-pool/internal/gateway"
	"github.com/Alfonsxh/codex-cpa-pool/internal/reasoningpolicy"
	"github.com/Alfonsxh/codex-cpa-pool/internal/snapshotfile"
	"github.com/google/uuid"
)

const (
	authSnapshotRelativePath = "state/gateway/auth-snapshot.json"
	defaultSnapshotGID       = 65534
)

type AuthSnapshotStore interface {
	ReadSettings(context.Context) (map[string]any, error)
	ReadAccounts(context.Context) ([]controlplane.Account, error)
	ReadRoutes(context.Context) (map[string]string, error)
	ReadKeyRecords(context.Context) ([]controlplane.KeyRecord, error)
	ReadInternalKeys(context.Context) (map[string]controlplane.InternalKey, error)
	EnsureInternalKeys(context.Context, []string) (map[string]controlplane.InternalKey, error)
}

type WriteFence interface {
	WithWriteFence(context.Context, func() error) error
}

type AuthSnapshotPublisher struct {
	Root         string
	Store        AuthSnapshotStore
	Fence        WriteFence
	ProbeURLs    []string
	HTTPClient   *http.Client
	Now          func() time.Time
	WaitTimeout  time.Duration
	PollInterval time.Duration
	SnapshotGID  int
}

func (publisher *AuthSnapshotPublisher) PublishAuthSnapshot(
	ctx context.Context,
	wait bool,
) (Snapshot, error) {
	if publisher == nil || publisher.Store == nil || publisher.Fence == nil {
		return Snapshot{}, errors.New("auth snapshot publisher requires a control-plane store and ownership fence")
	}
	records, err := publisher.Store.ReadKeyRecords(ctx)
	if err != nil {
		return Snapshot{}, fmt.Errorf("read auth snapshot users: %w", err)
	}
	activeUsers := activeRecordUsers(records)
	_, err = publisher.Store.EnsureInternalKeys(ctx, activeUsers)
	if err != nil {
		return Snapshot{}, fmt.Errorf("synchronize auth snapshot internal keys: %w", err)
	}
	var generation string
	if err := publisher.Fence.WithWriteFence(ctx, func() error {
		// The retained cross-process lock prevents route, account, Key, or
		// internal-Key writes from interleaving these reads with the atomic
		// snapshot replacement. Gateway activation is deliberately outside.
		accounts, err := publisher.Store.ReadAccounts(ctx)
		if err != nil {
			return fmt.Errorf("read auth snapshot accounts: %w", err)
		}
		routes, err := publisher.Store.ReadRoutes(ctx)
		if err != nil {
			return fmt.Errorf("read auth snapshot routes: %w", err)
		}
		records, err := publisher.Store.ReadKeyRecords(ctx)
		if err != nil {
			return fmt.Errorf("read auth snapshot Key records: %w", err)
		}
		internalKeys, err := publisher.Store.ReadInternalKeys(ctx)
		if err != nil {
			return fmt.Errorf("read auth snapshot internal keys: %w", err)
		}
		settings, err := publisher.Store.ReadSettings(ctx)
		if err != nil {
			return fmt.Errorf("read gateway request policy: %w", err)
		}
		limit, err := reasoningpolicy.FromSettings(settings)
		if err != nil {
			return err
		}
		payload, builtGeneration, err := publisher.buildPayload(accounts, routes, records, internalKeys, limit)
		if err != nil {
			return err
		}
		path, err := publisher.snapshotPath()
		if err != nil {
			return err
		}
		generation = builtGeneration
		return publisher.writeSnapshot(path, payload)
	}); err != nil {
		return Snapshot{}, err
	}
	if wait {
		if err := publisher.waitForGeneration(ctx, generation); err != nil {
			return Snapshot{}, err
		}
	}
	return Snapshot{Generation: generation}, nil
}

func (publisher *AuthSnapshotPublisher) buildPayload(
	accounts []controlplane.Account,
	routes map[string]string,
	records []controlplane.KeyRecord,
	internalKeys map[string]controlplane.InternalKey,
	maxReasoningEffort string,
) ([]byte, string, error) {
	accountCatalog := make(map[string]controlplane.Account, len(accounts))
	for _, account := range accounts {
		accountCatalog[account.ID] = account
	}
	byUser := make(map[string][]controlplane.KeyRecord)
	for _, record := range records {
		if record.Status != "active" {
			continue
		}
		user := strings.ToLower(strings.TrimSpace(record.User))
		if user != "" {
			byUser[user] = append(byUser[user], record)
		}
	}
	users := make([]string, 0, len(byUser))
	for user := range byUser {
		users = append(users, user)
	}
	sort.Strings(users)
	authRecords := make([]gateway.AuthRecord, 0, len(users))
	emitted := make(map[string]struct{})
	for _, user := range users {
		items := byUser[user]
		internal, found := internalKeys[user]
		if !found || internal.Status != "active" || !validInternalKey(internal.Key) {
			return nil, "", fmt.Errorf("active user %s has no valid internal key", user)
		}
		secrets := make(map[string]struct{})
		for _, item := range items {
			secrets[item.Key] = struct{}{}
		}
		pairs := make([]struct {
			key     string
			account string
		}, 0, len(items))
		if len(secrets) == 1 {
			account := strings.TrimSpace(routes[user])
			metadata, accountFound := accountCatalog[account]
			if !accountFound || !metadata.GroupEnabled {
				continue
			}
			for _, item := range items {
				if item.Account == account {
					pairs = append(pairs, struct {
						key     string
						account string
					}{key: item.Key, account: account})
					break
				}
			}
		} else {
			for _, item := range items {
				if _, accountFound := accountCatalog[item.Account]; accountFound {
					pairs = append(pairs, struct {
						key     string
						account string
					}{key: item.Key, account: item.Account})
				}
			}
		}
		for _, pair := range pairs {
			digestBytes := sha256.Sum256([]byte(pair.key))
			digest := hex.EncodeToString(digestBytes[:])
			if _, duplicate := emitted[digest]; duplicate {
				continue
			}
			emitted[digest] = struct{}{}
			authRecords = append(authRecords, gateway.AuthRecord{
				ExternalKeySHA256: digest,
				UserEmail:         user,
				Account:           pair.account,
				Backend:           "cliproxy-" + pair.account + ":8317",
				InternalKey:       internal.Key,
				Label:             user + ":" + pair.account,
			})
		}
	}
	generation := strings.ReplaceAll(uuid.NewString(), "-", "")
	now := time.Now
	if publisher.Now != nil {
		now = publisher.Now
	}
	snapshot := gateway.AuthSnapshot{
		Version:            1,
		Generation:         generation,
		GeneratedAt:        float64(now().Unix()),
		Records:            authRecords,
		MaxReasoningEffort: maxReasoningEffort,
	}
	payload, err := json.Marshal(snapshot)
	if err != nil {
		return nil, "", fmt.Errorf("encode auth snapshot: %w", err)
	}
	payload = append(payload, '\n')
	if _, err := gateway.ParseAuthSnapshot(bytes.NewReader(payload)); err != nil {
		return nil, "", fmt.Errorf("validate generated auth snapshot: %w", err)
	}
	return payload, generation, nil
}

func activeRecordUsers(records []controlplane.KeyRecord) []string {
	users := make([]string, 0)
	seen := make(map[string]struct{})
	for _, record := range records {
		if record.Status != "active" {
			continue
		}
		user := strings.ToLower(strings.TrimSpace(record.User))
		if user == "" {
			continue
		}
		if _, found := seen[user]; found {
			continue
		}
		seen[user] = struct{}{}
		users = append(users, user)
	}
	sort.Strings(users)
	return users
}

func validInternalKey(key string) bool {
	if !strings.HasPrefix(key, "cpa_internal_") || len(key) != len("cpa_internal_")+64 {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(key, "cpa_internal_"))
	return err == nil
}

func (publisher *AuthSnapshotPublisher) snapshotPath() (string, error) {
	if strings.TrimSpace(publisher.Root) == "" {
		return "", errors.New("auth snapshot root is required")
	}
	root, err := filepath.Abs(publisher.Root)
	if err != nil {
		return "", fmt.Errorf("resolve auth snapshot root: %w", err)
	}
	return filepath.Join(root, authSnapshotRelativePath), nil
}

func (publisher *AuthSnapshotPublisher) writeSnapshot(path string, payload []byte) error {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o750); err != nil {
		return fmt.Errorf("create auth snapshot directory: %w", err)
	}
	if err := os.Chmod(directory, 0o750); err != nil {
		return fmt.Errorf("secure auth snapshot directory: %w", err)
	}
	gid := publisher.snapshotGID()
	if os.Geteuid() == 0 {
		if err := os.Chown(directory, -1, gid); err != nil {
			return fmt.Errorf("assign auth snapshot directory group: %w", err)
		}
	}
	if err := snapshotfile.Write(path, payload, 0o640, gid); err != nil {
		return fmt.Errorf("publish auth snapshot atomically: %w", err)
	}
	if err := os.Chmod(path, 0o640); err != nil {
		return fmt.Errorf("secure auth snapshot: %w", err)
	}
	if os.Geteuid() == 0 {
		if err := os.Chown(path, -1, gid); err != nil {
			return fmt.Errorf("assign auth snapshot group: %w", err)
		}
	}
	return nil
}

func (publisher *AuthSnapshotPublisher) snapshotGID() int {
	if publisher.SnapshotGID > 0 {
		return publisher.SnapshotGID
	}
	if os.Geteuid() != 0 {
		return os.Getgid()
	}
	return defaultSnapshotGID
}

func (publisher *AuthSnapshotPublisher) waitForGeneration(ctx context.Context, generation string) error {
	urls := normalizedProbeURLs(publisher.ProbeURLs)
	if len(urls) == 0 {
		return errors.New("auth snapshot activation wait requires at least one Gateway probe URL")
	}
	timeout := publisher.WaitTimeout
	if timeout <= 0 {
		timeout = 8 * time.Second
	}
	interval := publisher.PollInterval
	if interval <= 0 {
		interval = 100 * time.Millisecond
	}
	client := publisher.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 2 * time.Second}
	}
	waitContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var lastError error
	for {
		for _, baseURL := range urls {
			active, err := probeAuthGeneration(waitContext, client, baseURL, generation)
			if err == nil && active {
				return nil
			}
			if err != nil {
				lastError = err
			}
		}
		timer := time.NewTimer(interval)
		select {
		case <-waitContext.Done():
			timer.Stop()
			return fmt.Errorf(
				"Gateway did not activate auth snapshot %s before timeout: %w",
				generation,
				errors.Join(waitContext.Err(), lastError),
			)
		case <-timer.C:
		}
	}
}

func normalizedProbeURLs(values []string) []string {
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{})
	for _, value := range values {
		value = strings.TrimRight(strings.TrimSpace(value), "/")
		parsed, err := url.Parse(value)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
			continue
		}
		if _, found := seen[value]; found {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func probeAuthGeneration(
	ctx context.Context,
	client *http.Client,
	baseURL string,
	generation string,
) (bool, error) {
	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		baseURL+"/__internal/snapshots",
		nil,
	)
	if err != nil {
		return false, err
	}
	request.Header.Set("Accept", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return false, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return false, fmt.Errorf("Gateway snapshot probe returned HTTP %d", response.StatusCode)
	}
	var status gateway.Status
	decoder := json.NewDecoder(io.LimitReader(response.Body, 1<<20))
	if err := decoder.Decode(&status); err != nil {
		return false, fmt.Errorf("decode Gateway snapshot status: %w", err)
	}
	return status.Auth.ActiveGeneration == generation, nil
}
