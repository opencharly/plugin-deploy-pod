package deploypod

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/opencharly/sdk/deploykit"
	"github.com/opencharly/sdk/kit"
	"github.com/opencharly/spec/container"
	"github.com/opencharly/spec/ops"
	"github.com/opencharly/spec/spec"
)

// engine_ops.go — the pod substrate's consumer of the ENGINE provider-class ops
// (spec/container/engine_ops.go): `start_plan` for a launch argv and `unit_emit` for the
// systemd-unit supervision file. The pod no longer composes a podman-shaped argv itself:
// the capability table supplies the binary, the keep-id/userns fact, the GPU flag style and
// the runtime handlers, and the spec body decides the argv shape. It is the SAME body the
// compiled-in engine providers and the out-of-tree plugin-nerdctl serve
// (container.InvokeEngineOp), so there is one engine implementation and many placements (R2).

// podEngineRun is the pod-side input to the engine `start_plan` op. Every field is resolved
// from the pod's own config; none of it names an engine.
type podEngineRun struct {
	Image      string
	Name       string
	Network    string
	WorkingDir string
	Hostname   string
	Restart    string
	Detach     bool
	Remove     bool
	KeepID     bool
	UID        int
	GID        int
	Ports      []string
	Volumes    []string
	Env        []string
	EnvFiles   []string
	Security   spec.SecurityConfig
	GPU        bool
	Entrypoint []string
	ExtraArgs  []string
}

// engineRunArgv builds a launch argv through the engine class's `start_plan` body and returns
// the engine's own run mode. A ROOTFUL target carries the capability row's privilege_prefix
// (data, never a hard-coded escalation command); every launch of an engine that declares
// runtime_handlers carries `--runtime <handler>` selected from that set, so a containerd launch
// never relies on the daemon's default handler.
func engineRunArgv(engine string, rootful bool, r podEngineRun) ([]string, string, error) {
	c, ok := container.EngineCapabilityFor(engine)
	if !ok {
		return nil, "", fmt.Errorf("engine.run: no capability row for %q", engine)
	}
	if rootful && !c.SupportsRootful {
		return nil, "", fmt.Errorf("engine %q does not support rootful launches", engine)
	}
	extra := make([]string, 0, 16)
	if r.Remove && !c.NoRemoveWithDetach {
		extra = append(extra, "--rm")
	}
	if r.Hostname != "" {
		extra = append(extra, "--hostname", r.Hostname)
	}
	if r.Restart != "" {
		extra = append(extra, "--restart", r.Restart)
	}
	if r.WorkingDir != "" {
		extra = append(extra, "-w", r.WorkingDir)
	}
	if r.GPU {
		extra = append(extra, kit.GPURunArgs(engine)...)
	}
	extra = append(extra, deploykit.SecurityArgs(r.Security)...)
	for _, m := range r.Security.Mounts {
		if after, ok := strings.CutPrefix(m, "tmpfs:"); ok {
			extra = append(extra, "--tmpfs", after)
		} else {
			extra = append(extra, "-v", m)
		}
	}
	if c.SupportsUsernsKeepID && c.UsernsKeepIDArg != "" && r.KeepID {
		// The spec body's keep-id arm appends the bare UsernsKeepIDArg; the pods need the
		// uid/gid mapping, so the full spelling is emitted here from the SAME capability fact.
		extra = append(extra, fmt.Sprintf("%s:uid=%d,gid=%d", c.UsernsKeepIDArg, r.UID, r.GID))
	}
	extra = append(extra, r.ExtraArgs...)

	req := spec.EngineStartPlanRequest{
		Name:      r.Name,
		Image:     r.Image,
		Engine:    engine,
		Network:   r.Network,
		Rootful:   rootful,
		Runtime:   engineDefaultRuntime(c),
		Detach:    r.Detach,
		Ports:     r.Ports,
		Volumes:   r.Volumes,
		Env:       envSliceToStrMap(r.Env),
		EnvFiles:  r.EnvFiles,
		ExtraArgs: extra,
		ImageArgs: r.Entrypoint,
	}
	params, err := json.Marshal(req)
	if err != nil {
		return nil, "", err
	}
	raw, err := container.InvokeEngineOp(engine, ops.OpEngineStartPlan, params)
	if err != nil {
		return nil, "", fmt.Errorf("engine start_plan (%s): %w", engine, err)
	}
	var rep spec.EngineStartPlanReply
	if err := json.Unmarshal(raw, &rep); err != nil {
		return nil, "", fmt.Errorf("engine start_plan (%s): decode reply: %w", engine, err)
	}
	if rep.Error != "" {
		return nil, "", fmt.Errorf("engine start_plan (%s): %s", engine, rep.Error)
	}
	argv := prependEngineGlobalArgs(c, rep.Argv)
	if rootful && len(argv) > 0 {
		prefix := strings.Fields(c.PrivilegePrefix)
		if len(prefix) > 0 {
			argv = append(prefix, argv...)
		}
	}
	return argv, rep.RunMode, nil
}

// engineDefaultRuntime selects the handler a launch uses when the deploy names none: the
// FIRST handler the engine declares. An engine with an empty runtime_handlers list (podman,
// docker) gets no `--runtime` at all — the same argv it had before.
func engineDefaultRuntime(c spec.EngineCapability) string {
	if len(c.RuntimeHandlers) == 0 {
		return ""
	}
	return c.RuntimeHandlers[0]
}

// engineGlobalArgs returns the engine CLI's connection flags, from the capability row. Only
// an engine with a separate daemon endpoint declares them (containerd: address + namespace),
// so podman and docker are unchanged.
func engineGlobalArgs(c spec.EngineCapability) []string {
	out := make([]string, 0, 2)
	if c.Address != "" {
		out = append(out, "--address", c.Address)
	}
	if c.Namespace != "" {
		out = append(out, "--namespace", c.Namespace)
	}
	return out
}

// prependEngineGlobalArgs inserts the connection flags BEFORE the subcommand, which is where
// the engine CLI accepts persistent flags (nerdctl: `nerdctl --address … --namespace … run …`).
func prependEngineGlobalArgs(c spec.EngineCapability, argv []string) []string {
	globals := engineGlobalArgs(c)
	if len(globals) == 0 || len(argv) == 0 {
		return argv
	}
	out := make([]string, 0, len(argv)+len(globals))
	out = append(out, argv[0])
	out = append(out, globals...)
	out = append(out, argv[1:]...)
	return out
}

// engineStopArgv builds `<binary> <globals> stop <name>`.
func engineStopArgv(engine, name string) []string {
	c, _ := container.EngineCapabilityFor(engine)
	return prependEngineGlobalArgs(c, []string{kit.EngineBinary(engine), "stop", name})
}

// engineRemoveArgv builds `<binary> <globals> rm -f <name>`.
func engineRemoveArgv(engine, name string) []string {
	c, _ := container.EngineCapabilityFor(engine)
	return prependEngineGlobalArgs(c, []string{kit.EngineBinary(engine), "rm", "-f", name})
}

// engineUnitFiles renders the supervision unit through the engine class's `unit_emit` body.
func engineUnitFiles(engine string, req spec.EngineUnitRequest) (map[string]string, error) {
	params, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	raw, err := container.InvokeEngineOp(engine, ops.OpEngineUnitEmit, params)
	if err != nil {
		return nil, fmt.Errorf("engine unit_emit (%s): %w", engine, err)
	}
	var rep spec.EngineUnitReply
	if err := json.Unmarshal(raw, &rep); err != nil {
		return nil, fmt.Errorf("engine unit_emit (%s): decode reply: %w", engine, err)
	}
	if rep.Error != "" {
		return nil, fmt.Errorf("engine unit_emit (%s): %s", engine, rep.Error)
	}
	return rep.Files, nil
}

// engineKeepID reports whether an engine's launch maps the invoking user into the container
// for host-identical bind mounts. It is the capability fact, not an engine-name switch.
func engineKeepID(engine string, bindMounts int) bool {
	c, ok := container.EngineCapabilityFor(engine)
	return ok && c.SupportsUsernsKeepID && c.UsernsKeepIDArg != "" && bindMounts > 0
}

// engineUsesCDI reports whether the engine's GPU passthrough uses the CDI device form. It is
// the capability's GPUArgStyle fact, so the CDI ensure leg is not gated on an engine name.
func engineUsesCDI(engine string) bool {
	c, ok := container.EngineCapabilityFor(engine)
	return ok && c.GPUArgStyle == "cdi"
}

// podRootful reports whether the host config asks for the rootful mode. `auto` is the
// pre-resolution selector; every concrete mode ("sudo", "native", "machine") is an explicit
// rootful request.
func podRootful(rt *kit.ResolvedRuntime) bool {
	return rt.Rootful != "" && rt.Rootful != "auto"
}

// rootfulRefusal is the honest answer where this plugin cannot finish a rootful deploy safely:
// system-scope units and a rootful image store are not implemented here, so silently running
// the rootless arm would deploy a different target than the operator asked for.
func rootfulRefusal(engine, mode string) error {
	return fmt.Errorf("engine %q: rootful %s deploys are not implemented by plugin-deploy-pod yet (system-scope unit emission and a rootful image store are missing); remedy: run rootless (engine.rootful=auto), or use engine: podman (quadlet) for a rootful pod", engine, mode)
}

// rootfulUnsupported reports the rootful targets this plugin refuses. It is capability-driven:
// a systemd-unit mode needs system-scope emission, and an engine with a privilege_prefix
// expects a root daemon the rootless staging path cannot reach. An engine with neither (podman
// quadlet) keeps its existing behaviour untouched.
func rootfulUnsupported(rt *kit.ResolvedRuntime) bool {
	if !podRootful(rt) {
		return false
	}
	if rt.RunMode == "systemd-unit" {
		return true
	}
	c, ok := container.EngineCapabilityFor(rt.RunEngine)
	return ok && c.PrivilegePrefix != ""
}

// envSliceToStrMap converts the pod's ordered `KEY=VALUE` env list to the request's map form.
// start_plan emits `-e KEY=VALUE` from it; order is not significant to the engine. A bare
// `KEY` (inherit-from-host) has no place in the map: the start_plan request only carries
// KEY=VALUE, and writeSystemdUnitConfig refuses a bare entry rather than dropping it.
func envSliceToStrMap(env []string) spec.StrMap {
	if len(env) == 0 {
		return nil
	}
	out := make(spec.StrMap, len(env))
	for _, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok {
			out[k] = v
		}
	}
	return out
}

// volumeArgs renders named volumes and bind mounts in the request's `-v` value form.
func volumeArgs(volumes []deploykit.VolumeMount, bindMounts []deploykit.ResolvedBindMount) []string {
	out := make([]string, 0, len(volumes)+len(bindMounts))
	for _, vol := range volumes {
		out = append(out, vol.VolumeName+":"+vol.ContainerPath)
	}
	for _, bm := range bindMounts {
		out = append(out, bm.HostPath+":"+bm.ContPath)
	}
	return out
}

// localizedPorts renders the deploy's port mappings for this host's bind address.
func localizedPorts(ports []string, bindAddr string) []string {
	out := make([]string, 0, len(ports))
	for _, p := range ports {
		out = append(out, deploykit.LocalizePort(p, bindAddr))
	}
	return out
}

// envFiles returns the singleton env-file list the request uses, or nil.
func envFiles(envFile string) []string {
	if envFile == "" {
		return nil
	}
	return []string{envFile}
}
