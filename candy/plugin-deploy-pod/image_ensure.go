package deploypod

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/opencharly/sdk"
	"github.com/opencharly/sdk/kit"
	"github.com/opencharly/spec/container"
	"github.com/opencharly/spec/spec"
)

// image_ensure.go — the pod-config-ensure-image leg, relocated from
// charly/host_build_pod_config_seams.go's hostBuildPodConfigEnsureImage/ensureImagePresent
// (K-wave W3a B6, the A2-proven peer-dispatch pattern: candy/plugin-preempt already reaches a
// COMPILED-IN provider word via a plain resolve+invoke; here the plugin reaches build:ensure the
// SAME way core's own dispatchBuildEnsure does, just via the plugin-to-plugin
// Executor.InvokeProvider leg instead of core's private providerRegistry). container.LocalImageExists
// / container.TransferImage are ALREADY portable (spec/container, used directly by candy/plugin-build
// per that package's own doc comment) — no seam needed for those tiers either. The
// "pod-config-ensure-image" HostBuild seam + its core handler are DELETED; dispatchBuildEnsure
// itself STAYS core (it has other core-internal callers: plugin_executor_reverse.go — the former
// check-run preflight caller is gone too, K-wave 2 cone R4: the check preflight now reaches
// build:ensure over the SAME InvokeProvider peer dispatch this file uses).
//
// THE ENGINE IS THE RESOLVED RUN ENGINE. It used to be a package constant naming podman, which
// made every pod stage its image into podman's store no matter what `engine.run` said, and then
// transfer it across stores. The pod now stages into the store of the engine it will actually
// launch on; `build:ensure` receives that same engine word.

// ensureImagePresent guarantees imageRef is available in the resolved run engine's local store,
// driving the store itself (no host round-trip): (1) already-present short-circuit, (2)
// build:ensure peer-dispatch (pulls from the registry, falling back to a local build for a
// project charly.yml entry).
func ensureImagePresent(ctx context.Context, ex *sdk.Executor, imageRef, runEngine string) error {
	engine := resolveRunEngine(runEngine)
	if container.LocalImageExists(engine, imageRef) {
		return nil
	}
	if err := dispatchBuildEnsurePeer(ctx, ex, imageRef, engine); err == nil {
		return nil
	}
	return fmt.Errorf("%w: %s", spec.ErrImageNotLocal, imageRef)
}

// resolveRunEngine is the ONE fallback for an unset engine word: the schema's default engine.
// The runtime resolver always yields a concrete word; this guards direct callers and tests
// without inventing a literal.
func resolveRunEngine(runEngine string) string {
	if runEngine == "" {
		return spec.DefaultContainerEngine
	}
	return runEngine
}

// currentRunEngine resolves the configured run engine for call sites that do not already carry
// a ResolvedRuntime. A resolution failure degrades to the schema default, never to a literal.
func currentRunEngine() string {
	if rt, err := kit.ResolveRuntime(); err == nil {
		return resolveRunEngine(rt.RunEngine)
	}
	return resolveRunEngine("")
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
