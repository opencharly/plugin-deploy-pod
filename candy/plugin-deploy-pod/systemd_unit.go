package deploypod

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/opencharly/sdk/deploykit"
	"github.com/opencharly/sdk/kit"
	"github.com/opencharly/spec/spec"
)

// systemd_unit.go — the `systemd-unit` run mode: an engine that has no quadlet generator
// (containerd, driven through nerdctl) is supervised by a plain systemd .service wrapping the
// engine CLI. The file's EXEC START is the engine `start_plan` argv (foreground, so systemd
// supervises the CLI for the container's lifetime); the file itself is rendered by the engine
// `unit_emit` body. Neither the argv nor the unit is podman-shaped here.

// writeSystemdUnitConfig emits the engine's supervision unit and (when configured) the
// cloudflare tunnel companion unit, then returns the written paths. The caller reloads the
// systemd user daemon and enables the tunnel exactly as it already does for the quadlet path.
func writeSystemdUnitConfig(rt *kit.ResolvedRuntime, qcfg deploykit.QuadletConfig, bindMounts []deploykit.ResolvedBindMount, sidecars []deploykit.ResolvedSidecar, tunnelCfg *spec.TunnelConfig) ([]string, error) {
	engine := rt.RunEngine
	if rootfulUnsupported(rt) {
		return nil, rootfulRefusal(engine, rt.RunMode)
	}
	// A bare `KEY` env entry means "inherit this variable from the host"; systemd's
	// Environment= has no inherit form and EngineUnitRequest carries no PassEnvironment
	// field, so the unit cannot represent it. Refuse it instead of silently dropping it.
	for _, kv := range qcfg.Env {
		if !strings.Contains(kv, "=") {
			return nil, fmt.Errorf("systemd-unit cannot inherit host environment variable %q: only KEY=VALUE entries are representable in a systemd unit (set the value explicitly)", kv)
		}
	}
	if len(sidecars) > 0 {
		fmt.Fprintf(os.Stderr, "Warning: sidecars are not supported in systemd-unit mode (skipping); use run_mode=quadlet for sidecar deploys.\n")
	}
	if deploykit.HasEncryptedBindMounts(bindMounts) {
		fmt.Fprintf(os.Stderr, "Warning: encrypted bind mounts are mounted by `charly start` in systemd-unit mode; no boot-time ExecStartPre mount is emitted (use run_mode=quadlet for that).\n")
	}

	name := kit.ContainerNameInstance(qcfg.BoxName, qcfg.Instance)
	workingDir := deploykit.ResolveWorkingDir(qcfg.Volumes, bindMounts, qcfg.Home, qcfg.BoxName, qcfg.Instance)
	argv, _, err := engineRunArgv(engine, false, podEngineRun{
		Image:      qcfg.ImageRef,
		Name:       name,
		Network:    qcfg.Network,
		WorkingDir: workingDir,
		Hostname:   name,
		Detach:     false,
		KeepID:     engineKeepID(engine, len(bindMounts)),
		UID:        qcfg.UID,
		GID:        qcfg.GID,
		Ports:      localizedPorts(qcfg.Ports, qcfg.BindAddress),
		Volumes:    volumeArgs(qcfg.Volumes, bindMounts),
		Env:        qcfg.Env,
		EnvFiles:   envFiles(qcfg.EnvFile),
		Security:   qcfg.Security,
		GPU:        qcfg.GPU,
		Entrypoint: qcfg.Entrypoint,
	})
	if err != nil {
		return nil, err
	}

	// unit_emit names the file "charly-<name>.service"; pass the container name without its
	// "charly-" prefix so the file IS the deploy's service name (kit.ServiceNameInstance), which
	// the existing start/stop and post_enable paths already use.
	unitName := strings.TrimPrefix(name, "charly-")
	files, err := engineUnitFiles(engine, spec.EngineUnitRequest{
		Name:        unitName,
		RunMode:     "systemd-unit",
		StartArgv:   argv,
		StopArgv:    engineStopArgv(engine, name),
		Description: "charly deployment " + name,
		WorkingDir:  workingDir,
	})
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("engine unit_emit (%s) returned no files for run mode systemd-unit", engine)
	}

	svcDir, err := kit.SystemdUserDir()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(svcDir, 0755); err != nil {
		return nil, fmt.Errorf("creating systemd user directory: %w", err)
	}
	written := make([]string, 0, len(files)+1)
	for file, body := range files {
		p := filepath.Join(svcDir, filepath.Base(file))
		// 0600, matching the quadlet writer: the ExecStart argv carries the deploy's env
		// (including provisioned secret values), which must not be world-readable.
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			return nil, fmt.Errorf("writing systemd unit %s: %w", p, err)
		}
		written = append(written, p)
	}
	if tunnelCfg != nil && tunnelCfg.Provider == "cloudflare" {
		p := filepath.Join(svcDir, deploykit.TunnelServiceFilename(qcfg.BoxName))
		if err := os.WriteFile(p, []byte(deploykit.GenerateTunnelUnit(qcfg, tunnelConfigPath(tunnelCfg.TunnelName))), 0o644); err != nil {
			return nil, fmt.Errorf("writing tunnel service file %s: %w", p, err)
		}
		written = append(written, p)
	}
	// Deterministic report order (the files map is unordered), so a re-run reads the same.
	sort.Strings(written)
	return written, nil
}
