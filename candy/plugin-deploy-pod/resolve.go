package deploypod

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/opencharly/sdk"
	"github.com/opencharly/sdk/deploykit"
	"github.com/opencharly/sdk/kit"
	"github.com/opencharly/spec/spec"
)

// resolve.go — the P13-KERNEL step-4(ii) direction-flip: the pod START/STOP plan resolution
// (formerly charly-core's pod_lifecycle_resolve.go resolvePodStartPlan/resolvePodStartQuadlet/
// resolvePodStartDirect/resolvePodStopPlan/resolvePodRuntimeImage) moved here VERBATIM — same
// sequencing, same argv-building logic (byte-diff gated against the pre-move source). The plugin
// self-resolves using #PodStartOpts/#PodStopOpts + the deploy key (box/instance, threaded via
// lifecycleParams.Name), calling back the narrow "pod-config-*" seams for host/loader/registry-
// coupled sub-steps (image ensure, device detection, deploy-config load, box-engine lookup, the
// enc-ensure/enc-unmount/container-tunnel credential/registry-coupled bundles).

// resolvePodStartPlan builds the pod START plan. The run mode is the runtime's resolved one:
// quadlet threads the systemd unit name, systemd-unit threads the engine-emitted service, and
// direct threads the engine `start_plan` argv.
func resolvePodStartPlan(ctx context.Context, ex *sdk.Executor, box, instance string, opts spec.PodStartOpts) (*spec.PodLifecyclePlan, error) {
	rt, err := kit.ResolveRuntime()
	if err != nil {
		return nil, err
	}
	switch rt.RunMode {
	case "quadlet":
		return resolvePodStartQuadlet(ctx, ex, box, instance, rt)
	case "systemd-unit":
		return resolvePodStartSystemdUnit(ctx, ex, box, instance, rt)
	default:
		return resolvePodStartDirect(ctx, ex, box, instance, rt, opts)
	}
}

// resolvePodStartSystemdUnit resolves the systemd-unit-mode start plan: the engine's emitted
// .service (unit_emit) is started with `systemctl --user start <svc>`, exactly like the quadlet
// path starts podman's generated unit. The unit file's existence is the "configured" signal.
func resolvePodStartSystemdUnit(ctx context.Context, ex *sdk.Executor, box, instance string, rt *kit.ResolvedRuntime) (*spec.PodLifecyclePlan, error) {
	dc, err := loadDeploy(ctx, ex, "charly start")
	if err != nil {
		return nil, err
	}
	sel, err := resolvePodSelectionFromConfig(dc, box, instance, rt)
	if err != nil {
		return nil, err
	}
	// The rootful refusal stays FIRST (as it was): the operator gets the reason they cannot
	// run this deployment, not a secondary "not configured".
	if rootfulUnsupported(sel.Target, rt.RunMode) {
		return nil, rootfulRefusal(sel.Engine, rt.RunMode)
	}
	svc := kit.ServiceNameInstance(box, instance)
	svcDir, err := kit.SystemdUserDir()
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(filepath.Join(svcDir, svc)); err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("not configured; run 'charly config %s' first", box)
		}
		return nil, err
	}
	plan := &spec.PodLifecyclePlan{
		Mode:          "systemd-unit",
		SvcName:       svc,
		ContainerName: kit.ContainerNameInstance(box, instance),
		EngineBin:     sel.binary(),
	}
	encJSON, err := resolvePodEncEnsurePlan(ctx, ex, dc, box, instance, false)
	if err != nil {
		return nil, err
	}
	plan.Enc = encJSON
	plan.Tunnel = resolvePodTunnelPlan(dc, box, instance)
	return plan, nil
}

// resolvePodStartQuadlet resolves the quadlet-mode start plan (the deployed/bed path): the plugin
// runs `systemctl --user start <svc>` (or `<engine> start <ctr>` through the plan's resolved
// engine binary for a direct-deploy marker) + mounts encrypted volumes.
func resolvePodStartQuadlet(ctx context.Context, ex *sdk.Executor, box, instance string, rt *kit.ResolvedRuntime) (*spec.PodLifecyclePlan, error) {
	exists, err := kit.QuadletExistsInstance(box, instance)
	if err != nil {
		return nil, err
	}
	directDeploy := !exists && IsDirectDeploy(box, instance)
	if !exists && !directDeploy {
		return nil, fmt.Errorf("not configured; run 'charly config %s' first", box)
	}
	dc, err := loadDeploy(ctx, ex, "charly start")
	if err != nil {
		return nil, err
	}
	sel, err := resolvePodSelectionFromConfig(dc, box, instance, rt)
	if err != nil {
		return nil, err
	}
	plan := &spec.PodLifecyclePlan{
		Mode:          "quadlet",
		SvcName:       kit.ServiceNameInstance(box, instance),
		ContainerName: kit.ContainerNameInstance(box, instance),
		DirectDeploy:  directDeploy,
		EngineBin:     sel.binary(),
	}
	// Encrypted-volume mounts are skipped in direct-deploy mode (those require systemd-run --scope).
	if !directDeploy {
		encJSON, err := resolvePodEncEnsurePlan(ctx, ex, dc, box, instance, false)
		if err != nil {
			return nil, err
		}
		plan.Enc = encJSON
	}
	plan.Tunnel = resolvePodTunnelPlan(dc, box, instance)
	return plan, nil
}

// podRuntimeImage is the resolved pod image context shared by the start-plan resolver and the F12
// shell resolver (R3): the runtime-detected devices, the RESOLVED selection (engine + target),
// the image ref + baked metadata, the per-host deploy config, and the resolved volume backing.
type podRuntimeImage struct {
	detected   spec.DetectedDevices
	sel        podSelection
	imageRef   string
	meta       *spec.BoxMetadata
	dc         *deploykit.DeployConfig
	volumes    []deploykit.VolumeMount
	bindMounts []deploykit.ResolvedBindMount
}

// resolvePodRuntimeImage resolves the pod's runtime image context — the identical HEAD both
// resolvePodStartDirect (`charly start` direct-mode) and resolvePodShellPlan (`charly shell`)
// compute: device detection + CDI, the deploy overlay, the image ref, and the volume backing.
// It loads the overlay and resolves the selection once, then hands both to
// resolvePodRuntimeImageWith.
func resolvePodRuntimeImage(ctx context.Context, ex *sdk.Executor, box, instance, tag string, rt *kit.ResolvedRuntime, noAutoDetect bool, volumeFlag, bind []string) (*podRuntimeImage, error) {
	dc, err := loadDeploy(ctx, ex, "charly pod runtime image")
	if err != nil {
		return nil, err
	}
	sel, err := resolvePodSelectionFromConfig(dc, box, instance, rt)
	if err != nil {
		return nil, err
	}
	return resolvePodRuntimeImageWith(ctx, ex, box, instance, tag, rt, dc, sel, noAutoDetect, volumeFlag, bind)
}

// resolvePodRuntimeImageWith resolves the image context against an already-loaded overlay and an
// already-resolved selection, so a caller that must refuse a rootful target BEFORE any image
// work does not pay a second overlay read.
func resolvePodRuntimeImageWith(ctx context.Context, ex *sdk.Executor, box, instance, tag string, rt *kit.ResolvedRuntime, dc *deploykit.DeployConfig, sel podSelection, noAutoDetect bool, volumeFlag, bind []string) (*podRuntimeImage, error) {
	// Device detection runs plugin-side (detect_devices.go — the relocated
	// "pod-config-detect-devices" seam). It receives the RESOLVED engine, so the CDI ensure
	// leg fires for the engine that will actually launch the container.
	detected := detectDevices(ctx, ex, noAutoDetect, string(sel.Engine))

	var deployVolumes []spec.DeployVolume
	if dc != nil {
		if overlay, ok := dc.Lookup(box, instance); ok {
			deployVolumes = overlay.Volume
		}
	}

	_, imageRef, err := resolveDeployRefLocal(ctx, ex, box, instance, tag, "")
	if err != nil {
		return nil, err
	}
	if err := ensureImagePresent(ctx, ex, imageRef, sel.Target); err != nil {
		return nil, err
	}
	metaPtr, err := deploykit.ExtractMetadata(string(sel.Engine), imageRef)
	if err != nil {
		return nil, err
	}
	if metaPtr == nil {
		return nil, fmt.Errorf("image %s has no embedded metadata; rebuild with latest charly", imageRef)
	}
	meta := *metaPtr
	if dc != nil {
		deploykit.MergeDeployOntoMetadata(&meta, dc, box, instance)
	}

	cliVolumes := parseVolumeFlagsCLI(volumeFlag, bind)
	volumes, bindMounts := deploykit.ResolveVolumeBacking(box, instance, meta.Volume, mergeVolumeConfigsLocal(deployVolumes, cliVolumes), meta.Home, rt.EncryptedStoragePath, rt.VolumesPath)
	if meta.Registry != "" {
		if _, ref, e := resolveDeployRefLocal(ctx, ex, box, instance, tag, ""); e == nil {
			imageRef = ref
		}
	}
	return &podRuntimeImage{detected: detected, sel: sel, imageRef: imageRef, meta: &meta, dc: dc, volumes: volumes, bindMounts: bindMounts}, nil
}

// resolvePodStartDirect resolves the direct-mode (non-unit) start plan: the launch argv the
// plugin execs, built by the engine `start_plan` body. It returns RunArgv = buildStartArgs(…)
// instead of running it, and threads the enc-ensure + tunnel legs. The sidecar-in-direct-mode
// rejection is preserved (sidecars require a unit run mode).
func resolvePodStartDirect(ctx context.Context, ex *sdk.Executor, box, instance string, rt *kit.ResolvedRuntime, opts spec.PodStartOpts) (*spec.PodLifecyclePlan, error) {
	dc, err := loadDeploy(ctx, ex, "charly start")
	if err != nil {
		return nil, err
	}
	sel, err := resolvePodSelectionFromConfig(dc, box, instance, rt)
	if err != nil {
		return nil, err
	}
	if rootfulUnsupported(sel.Target, rt.RunMode) {
		return nil, rootfulRefusal(sel.Engine, rt.RunMode)
	}
	img, err := resolvePodRuntimeImageWith(ctx, ex, box, instance, "", rt, dc, sel, opts.NoAutoDetect, opts.VolumeFlag, opts.Bind)
	if err != nil {
		return nil, err
	}
	detected, imageRef, meta := img.detected, img.imageRef, img.meta
	volumes, bindMounts := img.volumes, img.bindMounts

	if dc != nil {
		if overlay, ok := dc.Lookup(box, instance); ok && len(overlay.Sidecar) > 0 {
			return nil, fmt.Errorf("image %s has sidecars configured in charly.yml; use 'charly config %s && charly start %s' (sidecars require quadlet mode)", box, box, box)
		}
	}

	uid, gid, home := meta.UID, meta.GID, meta.Home
	ports := meta.Port
	security := meta.Security
	network := meta.Network
	entrypoint := kit.ResolveEntrypointFromMeta(meta)

	encJSON, err := resolvePodEncEnsurePlan(ctx, ex, dc, box, instance, false)
	if err != nil {
		return nil, err
	}
	if err := deploykit.VerifyBindMounts(bindMounts, box); err != nil {
		return nil, err
	}

	deployEnv := meta.Env
	startCtrName := kit.ContainerNameInstance(box, instance)
	startAccepted := deploykit.AcceptedEnvSet(meta.EnvAccept, meta.EnvRequire)
	var startGlobalEnv []string
	if dc != nil {
		startGlobalEnv = deploykit.GlobalEnvForImage(dc, spec.DeployKey(box, instance), startCtrName, startAccepted)
	}
	envVars, err := kit.ResolveEnvVars(startGlobalEnv, deployEnv, "", workspaceBindHost(bindMounts), opts.EnvFile, opts.Env)
	if err != nil {
		return nil, err
	}

	if !security.Privileged {
		security.Devices = deploykit.AppendUnique(security.Devices, detected.Devices...)
		if detected.AMDGPU {
			security.GroupAdd = appendGroupsForAMDGPU(security.GroupAdd)
		}
	}
	envVars = appendAutoDetectedEnv(envVars, detected)

	resolvedNetwork, netErr := kit.ResolveNetwork(network, string(sel.Engine))
	if netErr != nil {
		return nil, netErr
	}

	if len(opts.Port) > 0 {
		ports, err = kit.ApplyPortOverrides(ports, opts.Port)
		if err != nil {
			return nil, err
		}
		// #55 K4: write the recomputed ports PLUGIN-SIDE (deploykit.SaveDeployState), not over the
		// deleted "deploy-config-save-state" host seam.
		deploySaveState(ctx, ex, box, instance, spec.SaveDeployStateInput{Ports: ports, SetPorts: true})
	}
	if conflicts := kit.CheckPortAvailability(ports, rt.BindAddress, string(sel.Engine)); len(conflicts) > 0 {
		return nil, fmt.Errorf("port conflicts detected:%s", kit.FormatPortConflicts(conflicts, box))
	}

	var deployBox *spec.DeployNode
	if dc != nil {
		if overlay, ok := dc.Lookup(box, instance); ok {
			deployBox = &overlay
		}
	}
	agentFwd := kit.ResolveAgentForwarding(rt, deployBox, home)
	for _, v := range agentFwd.Volumes {
		security.Mounts = deploykit.AppendUnique(security.Mounts, v)
	}
	envVars = append(envVars, agentFwd.Env...)

	name := kit.ContainerNameInstance(box, instance)
	workDir := deploykit.ResolveWorkingDir(volumes, bindMounts, home, box, instance)
	argv, err := buildStartArgs(sel, imageRef, uid, gid, ports, name, volumes, bindMounts, detected.GPU, rt.BindAddress, envVars, security, entrypoint, workDir, resolvedNetwork)
	if err != nil {
		return nil, err
	}

	tunnel := resolvePodTunnelPlan(dc, box, instance)

	return &spec.PodLifecyclePlan{
		Mode: "direct", ContainerName: name, RunArgv: argv, EngineBin: sel.binary(),
		Enc: encJSON, Tunnel: tunnel,
	}, nil
}

// resolvePodStopPlan builds the pod STOP plan.
func resolvePodStopPlan(ctx context.Context, ex *sdk.Executor, box, instance string, unmount bool) (*spec.PodLifecyclePlan, error) {
	rt, err := kit.ResolveRuntime()
	if err != nil {
		return nil, err
	}
	quadletActive, _ := kit.QuadletExistsInstance(box, instance)
	dc, err := loadDeploy(ctx, ex, "charly stop")
	if err != nil {
		return nil, err
	}
	sel, err := resolvePodSelectionFromConfig(dc, box, instance, rt)
	if err != nil {
		return nil, err
	}
	plan := &spec.PodLifecyclePlan{
		ContainerName: kit.ContainerNameInstance(box, instance),
		SvcName:       kit.ServiceNameInstance(box, instance),
		EngineBin:     sel.binary(),
		Unmount:       unmount,
		Tunnel:        resolvePodTunnelPlan(dc, box, instance),
	}
	if rt.RunMode == "systemd-unit" {
		plan.Mode = "systemd-unit"
	} else if quadletActive {
		plan.Mode = "quadlet"
	} else {
		plan.Mode = "direct"
	}
	if unmount {
		encJSON, err := resolvePodEncUnmountPlan(dc, box, instance)
		if err != nil {
			return nil, err
		}
		plan.Enc = encJSON
	}
	return plan, nil
}
