package deploypod

import (
	"context"
	"fmt"
	"os"

	"github.com/opencharly/sdk"
	"github.com/opencharly/sdk/deploykit"
	"github.com/opencharly/sdk/kit"
	"github.com/opencharly/sdk/loaderkit"
	"github.com/opencharly/spec/spec"
)

// resolve_ref.go — the deploy-key→(box-name, image-ref) resolution the pod config/start paths do,
// resolved PLUGIN-SIDE (the deploy-config-resolve reverse-leg SEAM COLLAPSE, #55 Cone A Unit 2).
// The former host seam and its three host-side resolvers (charly/deploy.go, charly/host_build_pod_config_seams.go)
// are DELETED — see the CHANGELOG for the retired names. This file self-resolves using only what a
// plugin can reach placement-invariantly:
//   - the per-host deploy overlay via the EXISTING "pod-config-load-deploy" seam (loadDeploy → dc),
//   - the PROJECT fallback via the shared loaderkit.LoadUnifiedViaExecutor helper + the
//     "deploy-plugins-connect" seam's project dir (the SAME plugin-side loader path
//     candy/plugin-deploy's resolveTreeViaLoader uses), and
//   - kit.ResolveShellImageRef / kit.LocalImageExists (pure).
//
// The BOX NAME follows the former host resolvers' two-tier semantics: per-host Image (DeployKey
// then bare key) → project Deploy[box].Image → the key itself. The IMAGE REF is composed from that
// box by the ONE build-path rule (resolveDeployImageRef): box registry → namespace
// defaults.registry → box LEAF, so a namespace-qualified box ADDRESS is never handed to the engine
// as an image repo. The add_candy-overlay resolved_image preference still wins over the base-name
// resolution (DeployKey then bare key), gated on the overlay image actually existing locally.

const deployPluginsConnectKind = "deploy-plugins-connect"

// resolveDeployRefLocal is the plugin-side replacement for the former "pod-config-resolve-ref" host
// seam. explicitRef (set only by `charly deploy from-box`) short-circuits both outputs, exactly as
// the former seam did.
func resolveDeployRefLocal(ctx context.Context, ex *sdk.Executor, box, instance, tag, explicitRef string) (deployBoxName, imageRef string, err error) {
	if explicitRef != "" {
		return explicitRef, explicitRef, nil
	}
	dc, err := loadDeploy(ctx, ex, "pod resolve deploy ref")
	if err != nil {
		return "", "", err
	}
	deployKey := spec.DeployKey(box, instance)
	deployBoxName, _ = resolveDeployBoxNameForKey(ctx, ex, dc, deployKey)
	if ov := resolvedOverlayImage(dc, box, instance); ov != "" {
		rt, rtErr := kit.ResolveRuntime()
		if rtErr != nil {
			return "", "", rtErr
		}
		sel, selErr := resolvePodSelectionFromConfig(dc, box, instance, rt)
		if selErr != nil {
			return "", "", selErr
		}
		if kit.LocalImageExists(string(sel.Engine), ov) {
			return deployBoxName, ov, nil
		}
	}
	return deployBoxName, resolveDeployImageRef(ctx, ex, deployKey, deployBoxName, tag), nil
}

// resolveBaseBoxName returns the BASE BOX NAME a pod lifecycle op must build/deploy from. It is the
// ONE implementation of "a deploy KEY is never a box name" (R2): podPrepareVenue and podRebuild
// call it instead of spelling the image/deploy-name fallback themselves.
//
// An explicit image (the host resolved the deploy entry itself) wins untouched. Otherwise the
// deploy KEY is resolved through the deploy-key→box rule (resolveDeployBoxNameForKey). Handing the
// raw key to the build pipeline is what produced `unknown box "charly/check-sidecar-pod"`
// (opencharly/plugin-build#46).
//
// Only a genuinely unresolvable key falls back to the key itself (the key==image convention).
func resolveBaseBoxName(ctx context.Context, ex *sdk.Executor, name, image, caller string) string {
	if image != "" {
		return image
	}
	dc, err := loadDeploy(ctx, ex, caller)
	if err != nil {
		return name
	}
	if resolved, ok := resolveDeployBoxNameForKey(ctx, ex, dc, name); ok {
		return resolved
	}
	return name
}

// resolveDeployBoxNameForKey resolves a deploy KEY to the BOX name its deploy entry declares,
// trying every spelling a namespaced deploy can carry. It is the ONE "given a deploy key, which
// box?" rule: the config ref resolution (resolveDeployRefLocal) and the lifecycle paths
// (resolveBaseBoxName) both go through it, so no caller can hand a deploy key to the box
// namespace.
//
// A deploy has TWO spellings: the KEY is `ns/name` (spec.DeployKey), while the PROJECT projection
// keys a namespaced deploy `ns.name` (spec.UnifiedFile.Deploys). This tries, in order:
//   - the key's base/instance pair (per-host overlay DeployKey then bare key, then the project's
//     base entry — the pre-existing rule, unchanged);
//   - the exact deploy key (a local instance deploy keeps its slash spelling in the project map);
//   - the namespace-qualified form (`ns.name`), which is how the project stores a namespaced
//     deploy — the leg a fresh `deploy add` needs, before any per-host entry exists.
//
// The bool reports whether an entry was found. Callers keep their OWN fallback: the config path
// keeps the bare base name (the key==image convention), the lifecycle paths keep the key.
func resolveDeployBoxNameForKey(ctx context.Context, ex *sdk.Executor, dc *deploykit.DeployConfig, key string) (string, bool) {
	box, instance := spec.ParseDeployKey(key)
	if img := deployKeyToBoxLocal(ctx, ex, dc, box, instance); img != "" {
		return img, true
	}
	if instance == "" {
		return box, false
	}
	if img := deployKeyToBoxLocal(ctx, ex, dc, key, ""); img != "" {
		return img, true
	}
	if img := deployKeyToBoxLocal(ctx, ex, dc, box+"."+instance, ""); img != "" {
		return img, true
	}
	return box, false
}

// projectConnect and loadUnifiedForBox are the two executor-backed legs of the project
// fallback, held as vars so a test can drive their FAILURE branches — the branches that only
// fire on a transport/decode fault and are therefore unreachable from a passing bed run. Same
// seam idiom the sibling config_setup paths use (loadProjectVolume, saveDeploy, mutateDeploy).
var (
	projectConnect = func(ctx context.Context, ex *sdk.Executor, box string, pre *spec.DeployPluginsConnectReply) error {
		return hostBuild(ctx, ex, deployPluginsConnectKind, spec.DeployPluginsConnectRequest{Path: box}, pre)
	}
	loadUnifiedForBox = loaderkit.LoadUnifiedViaExecutor
)

// deployKeyToBoxLocal mirrors the former host deploy-key→box loader-read: user (per-host) overlay
// wins over the project config; each is tried DeployKey-then-bare-key.
func deployKeyToBoxLocal(ctx context.Context, ex *sdk.Executor, dc *deploykit.DeployConfig, box, instance string) string {
	if dc != nil {
		if e, ok := dc.Lookup(box, instance); ok && e.Image != "" {
			return e.Image
		}
		if e, ok := dc.LookupKey(box); ok && e.Image != "" {
			return e.Image
		}
	}
	// Project-level fallback — the loader-over-executor path (parity with the former host resolver's
	// LoadUnified → ProjectDeployConfig). Degrades to "" (→ the key) on any connect/load failure,
	// matching the former resolver's no-entry → "" outcome.
	var pre spec.DeployPluginsConnectReply
	if err := projectConnect(ctx, ex, box, &pre); err != nil || pre.Dir == "" {
		// A genuine transport/decode failure is NOT the same as "this project declares no
		// entry", and silently conflating them is what made a spec/sdk pin skew between charly
		// and this out-of-process plugin present as a bogus `image not found in local storage:
		// <deploy-key>:<tag>` — a ref nothing ever built, because the box name had degraded to
		// the deploy key. Name the cause at the moment it is known.
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: deploy-key %q box resolution: project connect failed (%v); falling back to the key as the box name. If this plugin's spec/sdk pins are behind charly's, that skew is the usual cause.\n", box, err)
		}
		return ""
	}
	uf, ok, err := loadUnifiedForBox(ctx, ex, pre.Dir)
	if err != nil || !ok || uf == nil {
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: deploy-key %q box resolution: loading the project at %s failed (%v); falling back to the key as the box name.\n", box, pre.Dir, err)
		}
		return ""
	}
	pc := deploykit.ProjectDeployConfig(uf)
	if pc == nil {
		return ""
	}
	if e, ok := pc.Deploy[box]; ok && e.Image != "" {
		return e.Image
	}
	return ""
}

// resolveDeployImageRef composes the image ref a pod deploy resolves to, from the BOX the deploy
// names. It is the plugin side of the ONE box→image naming rule the host build path applies
// (charly's resolveBoxImageRegistryName + ResolveShellImageRef): the box's OWN registry, falling
// back to its NAMESPACE's defaults.registry, and the box LEAF.
//
// A namespace-qualified box ADDRESS is not an image repo. `charly.check-k8s-deploy-app` builds as
// `ghcr.io/opencharly/check-k8s-deploy-app:<tag>`; composing `<address>:<tag>` hands the engine a
// ref nothing produced (measured, opencharly/plugin-deploy-pod#44 run 2026.307.2336:
// `image not found in local storage: charly.check-k8s-deploy-app:<tag>`).
//
// A FULL ref (a deploy entry's explicit `image:`, Pattern B) is not a box name and keeps the
// pre-existing shell-resolve contract. A project that cannot be loaded degrades to the leaf-only
// bare ref — the resolver's existing best-effort contract, and the same name the build uses with
// no registry.
//
// This runs BEFORE any existence check (ensure-image / ExtractMetadata), so the ref handed to the
// engine is the one the build produced. A post-hoc qualification sourced from the IMAGE's OCI
// label is unreachable on this path for exactly that reason, and is deleted.
func resolveDeployImageRef(ctx context.Context, ex *sdk.Executor, deployKey, boxRef, tag string) string {
	if kit.LooksLikeFullRef(boxRef) {
		return kit.ResolveShellImageRef("", boxRef, tag)
	}
	return kit.ResolveShellImageRef(projectBoxRegistry(ctx, ex, deployKey, boxRef), spec.LeafName(boxRef), tag)
}

// projectBoxRegistry returns the registry the BOX BUILD prefixes for boxRef: the box's own
// `registry:`, else its NAMESPACE's `defaults.registry`, else "" (no registry). It reads the SAME
// project the deploy-key→box rule reads, over the SAME reverse-channel loader seams
// (projectConnect + loadUnifiedForBox), and folds the namespace tree through ProjectConfig.
func projectBoxRegistry(ctx context.Context, ex *sdk.Executor, deployKey, boxRef string) string {
	var pre spec.DeployPluginsConnectReply
	if err := projectConnect(ctx, ex, deployKey, &pre); err != nil || pre.Dir == "" {
		return ""
	}
	uf, ok, err := loadUnifiedForBox(ctx, ex, pre.Dir)
	if err != nil || !ok || uf == nil {
		return ""
	}
	cfg := uf.ProjectConfig()
	if cfg == nil {
		return ""
	}
	img, nsCfg, ok := cfg.ResolveBoxRef(boxRef)
	if !ok {
		// The project deploy map's `image:` can carry the box's LEAF (the bed declares
		// `image: check-k8s-deploy-app` while the box lives in the `charly` namespace). A bare
		// leaf does not resolve from the ROOT Config, so discover its namespaced address first —
		// the same discovery the host build path uses (Config.FindBoxByLeaf).
		if qualified, found := cfg.FindBoxByLeaf(spec.LeafName(boxRef)); found {
			img, nsCfg, ok = cfg.ResolveBoxRef(qualified)
		}
	}
	if !ok {
		return ""
	}
	if img.Registry != "" {
		return img.Registry
	}
	if nsCfg != nil {
		return nsCfg.Defaults.Registry
	}
	return ""
}

// resolvedOverlayImage mirrors the former host resolved_image lookup: the concrete add_candy-overlay
// image ref persisted per-host (DeployNode.ResolvedImage), DeployKey-then-bare-key.
func resolvedOverlayImage(dc *deploykit.DeployConfig, box, instance string) string {
	if dc == nil {
		return ""
	}
	if e, ok := dc.Lookup(box, instance); ok && e.ResolvedImage != "" {
		return e.ResolvedImage
	}
	if e, ok := dc.LookupKey(box); ok && e.ResolvedImage != "" {
		return e.ResolvedImage
	}
	return ""
}
