package deploypod

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/opencharly/sdk"
	"github.com/opencharly/sdk/kit"
	pb "github.com/opencharly/spec/proto"
	"github.com/opencharly/spec/spec"
)

// config_remove.go — sdk.OpConfigRemove: the former charly-core BoxConfigRemoveCmd.Run ported
// VERBATIM (no seams needed — purely resolveBoxName + IsDirectDeploy + podman/systemctl exec +
// findPodSidecarQuadlets, all portable). Distinct from `charly remove`/OpPostTeardown, which tears
// down the whole deploy; this disables the quadlet service (the quadlet file remains).
func invokeConfigRemove(ctx context.Context, req *pb.InvokeRequest) (*pb.InvokeReply, error) {
	// The executor is OPTIONAL here: remove is driven from the runtime configuration and
	// needs no venue work, so an Invoke with no executor attached must keep working — it
	// simply resolves the selection from the runtime level (loadDeploy degrades to nil).
	ex, _ := sdk.ExecutorFromInvoke(req.GetExecutorBrokerId())
	var r spec.PodConfigRemoveRequest
	if err := json.Unmarshal(req.GetParamsJson(), &r); err != nil {
		return nil, fmt.Errorf("plugin-deploy-pod config-remove: decode request: %w", err)
	}
	if err := runPodConfigRemove(ctx, ex, &r); err != nil {
		return nil, err
	}
	return marshalReply(spec.PodConfigRemoveReply{})
}

func runPodConfigRemove(ctx context.Context, ex *sdk.Executor, c *spec.PodConfigRemoveRequest) error {
	rt, err := kit.ResolveRuntime()
	if err != nil {
		return err
	}
	boxName := resolveBoxName(c.Box)
	dc, err := loadDeploy(ctx, ex, "charly config remove")
	if err != nil {
		return err
	}
	sel, err := resolvePodSelectionFromConfig(dc, boxName, c.Instance, rt)
	if err != nil {
		return err
	}

	if rt.RunMode == "direct" || IsDirectDeploy(boxName, c.Instance) {
		name := kit.ContainerNameInstance(boxName, c.Instance)
		stop := engineStopArgv(sel.Target, name)
		_ = exec.Command(stop[0], stop[1:]...).Run()
		rm := engineRemoveArgv(sel.Target, name)
		if out, err := exec.Command(rm[0], rm[1:]...).CombinedOutput(); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: %s rm -f %s: %v\n%s", sel.Engine, name, err, strings.TrimSpace(string(out)))
		}
		if err := removeDirectDeployMarker(boxName, c.Instance); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: removing direct-mode marker: %v\n", err)
		}
		fmt.Fprintf(os.Stderr, "Removed %s (direct mode)\n", name)
		return nil
	}

	if rt.RunMode == "systemd-unit" {
		svc := kit.ServiceNameInstance(boxName, c.Instance)
		cmd := exec.Command("systemctl", "--user", "disable", "--now", svc)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		_ = cmd.Run()
		// Same contract as the quadlet branch below (and its doc comment above):
		// `charly config remove` ONLY disables the service — the emitted unit file remains,
		// so the deploy stays configured and can be started again. `charly remove` is the
		// complete teardown that deletes the unit and reloads systemd
		// (charly/CHANGELOG/2026.221.2032.md).
		fmt.Fprintf(os.Stderr, "Disabled %s\n", svc)
		return nil
	}

	if rt.RunMode != "quadlet" {
		return fmt.Errorf("charly config remove requires run_mode=quadlet, systemd-unit, or direct (current: %s)", rt.RunMode)
	}

	svc := kit.ServiceNameInstance(boxName, c.Instance)
	cmd := exec.Command("systemctl", "--user", "disable", "--now", svc)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	_ = cmd.Run()

	podSvc := kit.PodNameInstance(boxName, c.Instance) + "-pod.service"
	_ = exec.Command("systemctl", "--user", "disable", "--now", podSvc).Run()

	if qdir, qErr := kit.QuadletDir(); qErr == nil {
		podName := kit.PodNameInstance(boxName, c.Instance)
		mainFile := kit.ContainerNameInstance(boxName, c.Instance) + ".container"
		if sidecars, dErr := findPodSidecarQuadlets(qdir, podName, mainFile); dErr == nil {
			for _, name := range sidecars {
				scSvc := strings.TrimSuffix(name, ".container") + ".service"
				fmt.Fprintf(os.Stderr, "Disabling sidecar %s\n", scSvc)
				_ = exec.Command("systemctl", "--user", "disable", "--now", scSvc).Run()
			}
		}
	}

	fmt.Fprintf(os.Stderr, "Disabled %s\n", svc)
	return nil
}
