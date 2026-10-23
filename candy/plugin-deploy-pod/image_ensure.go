package deploypod

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/opencharly/sdk"
	"github.com/opencharly/sdk/deploykit"
	"github.com/opencharly/spec/spec"
)

// image_ensure.go — the pod-config-ensure-image leg, relocated from
// charly/host_build_pod_config_seams.go's hostBuildPodConfigEnsureImage/ensureImagePresent
// (K-wave W3a B6, the A2-proven peer-dispatch pattern: candy/plugin-preempt already reaches a
// COMPILED-IN provider word via a plain resolve+invoke; here the plugin reaches build:ensure the
// SAME way core's own dispatchBuildEnsure does, just via the plugin-to-plugin
// Executor.InvokeProvider leg instead of core's private providerRegistry).
//
// THE STORE IS THE W3.7 ONE. The presence probe is deploykit's engine-neutral ImageStore, built
// from the RESOLVED target (selection.go) — not container.LocalImageExists over an engine word.
// The store addresses the engine CLI through the capability row (binary, presence argv, daemon
// address, namespace), so a containerd deploy probes the resolved namespace instead of assuming
// the capability default, and a daemon failure is surfaced rather than folded into "absent".
//
// The staging step stays build:ensure (W3.6), which receives the SAME resolved engine and lands
// the image in the store this file just probed. The sdk's ImageResolver.Build hook expresses
// that step for a caller that owns a CharlyImageSource (the engine-neutral built-image store);
// no production implementation of that source exists at this pin (opencharly/sdk#460), so the
// documented W3.7 order — target store, then build:ensure — is driven here instead of through
// the Build hook.

// reverseDeployExecutor adapts the plugin-side executor handle to the spec.DeployExecutor
// surface deploykit.NewImageStore addresses. The image store calls RunCapture (and nothing
// else), so every other method fails loudly instead of inventing a local answer.
type reverseDeployExecutor struct{ ex *sdk.Executor }

func (e reverseDeployExecutor) Venue() string { return "plugin-reverse-channel" }
func (e reverseDeployExecutor) Kind() string  { return "plugin-reverse-channel" }

func (e reverseDeployExecutor) RunCapture(ctx context.Context, script string) (string, string, int, error) {
	return e.ex.RunCapture(ctx, script)
}

func (e reverseDeployExecutor) RunSystem(context.Context, string, spec.EmitOpts) error {
	return spec.ErrNotSupported
}

func (e reverseDeployExecutor) RunUser(context.Context, string, spec.EmitOpts) error {
	return spec.ErrNotSupported
}

func (e reverseDeployExecutor) RunBuilder(context.Context, spec.BuilderRunOpts) ([]byte, error) {
	return nil, spec.ErrNotSupported
}

func (e reverseDeployExecutor) PutFile(context.Context, string, string, uint32, bool, spec.EmitOpts) error {
	return spec.ErrNotSupported
}

func (e reverseDeployExecutor) GetFile(context.Context, string, bool, spec.EmitOpts) ([]byte, error) {
	return nil, spec.ErrNotSupported
}

func (e reverseDeployExecutor) RunInteractive(context.Context, string) (int, error) {
	return 0, spec.ErrNotSupported
}

func (e reverseDeployExecutor) RunStream(context.Context, string) (int, error) {
	return 0, spec.ErrNotSupported
}

func (e reverseDeployExecutor) ResolveHome(context.Context, string) (string, error) {
	return "", spec.ErrNotSupported
}

// newImageStore builds the W3.7 store for an already-resolved target.
func newImageStore(ex *sdk.Executor, target deploykit.ContainerdTarget) (deploykit.ImageStore, error) {
	if ex == nil {
		return nil, fmt.Errorf("image store: no executor")
	}
	return deploykit.NewImageStore(reverseDeployExecutor{ex: ex}, target)
}

// ensureImagePresent guarantees imageRef is available in the RESOLVED target store, driving the
// store itself (no host round-trip): (1) the target store already addresses it, (2) otherwise
// build:ensure (W3.6) stages it — pulling from the registry or building locally — and the store
// is re-probed to prove the boundary.
func ensureImagePresent(ctx context.Context, ex *sdk.Executor, imageRef string, target deploykit.ContainerdTarget) error {
	store, err := newImageStore(ex, target)
	if err != nil {
		return err
	}
	// W3.7's resolution order starts at the target store. ResolveImage answers the
	// "absent" case, but it folds a probe FAILURE into the same error, so re-probe on the
	// error path to keep a daemon fault distinct from a confirmed miss.
	resolver := &deploykit.ImageResolver{}
	if _, err := resolver.ResolveImage(ctx, imageRef, store); err == nil {
		return nil
	}
	present, err := store.Has(ctx, imageRef)
	if err != nil {
		return err
	}
	if present {
		return nil
	}
	// Step 4 — build:ensure. It receives the SAME resolved engine word, so the image lands in
	// the store just probed.
	if err := dispatchBuildEnsurePeer(ctx, ex, imageRef, string(target.Engine)); err != nil {
		return fmt.Errorf("%w: %s: %v", spec.ErrImageNotLocal, imageRef, err)
	}
	present, err = store.Has(ctx, imageRef)
	if err != nil {
		return err
	}
	if !present {
		return fmt.Errorf("%w: %s (build:ensure did not stage it into the %s store)", spec.ErrImageNotLocal, imageRef, target.Engine)
	}
	return nil
}

// dispatchBuildEnsurePeer InvokeProviders the compiled-in build:ensure word — the SAME word
// core's own dispatchBuildEnsure (charly/dispatch_build_ensure.go, unchanged, still used by its
// other callers) reaches via the core-private providerRegistry; a plugin reaches it via the
// generic Executor.InvokeProvider peer-dispatch leg instead (class-agnostic — "build"/"ensure" is
// treated identically to any other (class, word) pair). The request carries the SAME run engine
// the pod will launch on, so the image lands in the store that runs it.
func dispatchBuildEnsurePeer(ctx context.Context, ex *sdk.Executor, imageRef, runEngine string) error {
	params, err := json.Marshal(spec.BuildEnsureRequest{
		Image:     imageRef,
		RunEngine: runEngine,
	})
	if err != nil {
		return err
	}
	out, err := ex.InvokeProvider(ctx, "build", "ensure", sdk.OpBuild, params, nil, sdk.InvokeProviderOpts{})
	if err != nil {
		return err
	}
	var reply spec.BuildEnsureReply
	if len(out) > 0 {
		if uerr := json.Unmarshal(out, &reply); uerr != nil {
			return uerr
		}
	}
	if reply.Error != "" {
		return fmt.Errorf("%s", reply.Error)
	}
	return nil
}
