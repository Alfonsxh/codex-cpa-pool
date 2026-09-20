package runtimeops

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Alfonsxh/codex-cpa-pool/internal/cpaplugin"
	"github.com/google/uuid"
	"github.com/moby/moby/api/pkg/stdcopy"
	containertypes "github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	dockerclient "github.com/moby/moby/client"
)

// ValidatePlugin loads the downloaded native code in the account's exact image,
// with no network, credentials or business mounts, before any live config changes.
func (runtime *AccountRuntime) ValidatePlugin(ctx context.Context, accountID, version string) error {
	if !cpaplugin.VersionPattern.MatchString(version) {
		return errors.New("invalid plugin version")
	}
	container, found, err := runtime.findAccountContainer(ctx, accountID)
	if err != nil {
		return err
	}
	if !found || container.State != "running" {
		return errors.New("plugin account is not running")
	}
	image := containerImageID(container)
	if image == "" {
		return errors.New("plugin probe requires an immutable CPA image")
	}
	dir, err := os.MkdirTemp(filepath.Join(runtime.root, "configs", accountID), ".plugin-check-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	config := `port: 8317
auth-dir: /tmp/empty-auth
commercial-mode: true
logging-to-file: false
remote-management:
  disable-control-panel: true
plugins:
  enabled: true
  dir: /probe-plugins
  configs:
    codex-ticket:
      enabled: true
      harvest_enabled: false
      inject_enabled: false
      host_config_file: /probe/config.yaml
      models: gpt-6-astra
      replace_existing: false
`
	if err = os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(config), 0o600); err != nil {
		return err
	}
	limit := int64(64)
	probeCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	created, err := runtime.client.ContainerCreate(probeCtx, dockerclient.ContainerCreateOptions{
		Name:   "cpa-plugin-check-" + uuid.NewString()[:8],
		Config: &containertypes.Config{Image: image, Entrypoint: []string{"/CLIProxyAPI/CLIProxyAPI"}, Cmd: []string{"-config", "/probe/config.yaml"}, NetworkDisabled: true, Labels: map[string]string{composeProjectLabel: runtime.project, "io.codex-cpa.managed-by": "codex-cpa", "io.codex-cpa.operation": "plugin-validation"}},
		HostConfig: &containertypes.HostConfig{NetworkMode: "none", ReadonlyRootfs: true, CapDrop: []string{"ALL"}, SecurityOpt: []string{"no-new-privileges"}, Resources: containertypes.Resources{PidsLimit: &limit, Memory: 256 << 20}, RestartPolicy: containertypes.RestartPolicy{Name: containertypes.RestartPolicyDisabled}, Tmpfs: map[string]string{"/tmp": "rw,nosuid,noexec,size=16m"}, Mounts: []mount.Mount{
			{Type: mount.TypeBind, Source: dir, Target: "/probe", ReadOnly: true},
			{Type: mount.TypeBind, Source: filepath.Join(runtime.root, "configs", accountID, "plugins", version), Target: "/probe-plugins", ReadOnly: true},
		}},
	})
	if err != nil {
		return fmt.Errorf("create isolated plugin probe: %w", err)
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_, _ = runtime.client.ContainerRemove(cleanup, created.ID, dockerclient.ContainerRemoveOptions{Force: true})
	}()
	if _, err = runtime.client.ContainerStart(probeCtx, created.ID, dockerclient.ContainerStartOptions{}); err != nil {
		return err
	}
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-probeCtx.Done():
			return errors.New("plugin failed isolated compatibility check")
		case <-ticker.C:
		}
		logs, err := runtime.client.ContainerLogs(probeCtx, created.ID, dockerclient.ContainerLogsOptions{ShowStdout: true, ShowStderr: true, Tail: "100"})
		if err != nil {
			return err
		}
		output := &limitedBannerOutput{limit: 64 << 10}
		_, copyErr := stdcopy.StdCopy(output, output, logs)
		logs.Close()
		if copyErr != nil {
			return copyErr
		}
		for _, line := range strings.Split(output.String(), "\n") {
			if strings.Contains(line, "plugin registered") && strings.Contains(line, "plugin_id=codex-ticket") && strings.Contains(line, "version="+strings.TrimPrefix(version, "v")+" ") {
				return nil
			}
		}
		state, err := runtime.client.ContainerInspect(probeCtx, created.ID, dockerclient.ContainerInspectOptions{})
		if err != nil {
			return err
		}
		if state.Container.State == nil || !state.Container.State.Running {
			return errors.New("CPA exited during isolated plugin check")
		}
	}
}
