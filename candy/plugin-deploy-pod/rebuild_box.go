package deploypod

// rebuild_box.go — which BOX `charly update <deploy> --build` rebuilds, and what happens when
// there is none.
//
// `charly update <name> --build` promises "rebuilds the artifact first". The build leg is a
// `charly box build <box>`, but a deploy entry records an IMAGE ref, not a box:
// `image: ghcr.io/opencharly/whisper:2026.325.2318`, or a bare `image: whisper`. Feeding such a
// ref to `box build` is at best an "unknown box" error and at worst a silent no-op while the
// unit/restart legs still run — the container is recreated on the OLD image and nothing says the
// rebuild did not happen (opencharly/charly#1297).
//
// So the rebuild resolves the box from what the deploy and its project already declare, in the
// order they can be trusted, and REFUSES before touching anything when neither names a box:
//
//	1. the recorded image, when it is a BARE name the project declares as a box
//	   (`image: whisper`, `image: comfyui-cdi` — the bed shapes);
//	2. the deploy key's base name, when the project declares a box by it
//	   (`whisper` → `box/whisper`, the owner-repo shape the CDI boxes moved to);
//	3. otherwise a refusal naming the recorded ref, the name it looked for, and the remedy.
//
// No ref PARSING is involved on purpose: the box never has to be derived from a registry ref,
// because the deploy key and the bare-image form already carry it, and a locally re-implemented
// "strip registry/tag" rule would duplicate deploykit's own (R2).

import (
	"context"
	"fmt"

	"github.com/opencharly/sdk"
	"github.com/opencharly/spec/spec"
)

// resolveRebuildBoxName returns the box the `--build` leg must rebuild, or an error naming what
// was NOT rebuilt. It never returns a name it has not confirmed: a name handed to `box build`
// that names nothing is exactly the silent skip this closes.
func resolveRebuildBoxName(ctx context.Context, ex *sdk.Executor, deployKey, caller string) (string, error) {
	recorded := recordedImageForDeployKey(ctx, ex, deployKey, caller)
	box, _ := spec.ParseDeployKey(deployKey)
	// The project is resolved the SAME way deployKeyToBoxLocal does it: a box name goes through
	// projectConnect to a DIRECTORY, and the directory is what the unified loader reads. Passing
	// the box name to the loader directly reads nothing and would refuse every real rebuild.
	var pre spec.DeployPluginsConnectReply
	loadErr := projectConnect(ctx, ex, box, &pre)
	uf, ok := (*spec.UnifiedFile)(nil), false
	if loadErr == nil && pre.Dir != "" {
		var uerr error
		uf, ok, uerr = loadUnifiedForBox(ctx, ex, pre.Dir)
		if loadErr == nil {
			loadErr = uerr
		}
	}
	declared := func(name string) bool {
		if !ok || uf == nil || name == "" {
			return false
		}
		_, found := uf.Box[name]
		return found
	}
	if candidate, ok := rebuildBoxCandidate(recorded, box, declared); ok {
		return candidate, nil
	}
	return "", fmt.Errorf("charly update %s --build: no box to rebuild — the deploy records the image %q and this project declares no box named %q or %q, so NOTHING WAS BUILT and the deploy must not be restarted as if it had been. Build it explicitly (`charly box build <box>`) or record the box's bare name on the deploy%s",
		deployKey, recorded, recorded, box, projectLoadNote(loadErr))
}

// rebuildBoxCandidate is the ONE decision "which declared name is this rebuild's box", pure so it
// can be exercised without a project. The recorded bare image name comes first: a bed records
// `image: whisper` while its deploy key is `check-whisper-pod`, so the key alone would miss the
// box the image names.
func rebuildBoxCandidate(recorded, box string, declared func(string) bool) (string, bool) {
	if declared(recorded) {
		return recorded, true
	}
	if declared(box) {
		return box, true
	}
	return "", false
}

// recordedImageForDeployKey reports the image the deploy records, through the SAME host→project
// resolution every other lifecycle path uses (resolveBaseBoxName → deployKeyToBoxLocal) — never a
// second lookup. The resolver falls back to the deploy KEY when no entry records an image; that
// fallback is reported as "unknown" here rather than as a recorded ref, so the refusal names what
// is actually known.
func recordedImageForDeployKey(ctx context.Context, ex *sdk.Executor, deployKey, caller string) string {
	resolved := resolveBaseBoxName(ctx, ex, deployKey, "", caller)
	box, _ := spec.ParseDeployKey(deployKey)
	if resolved == box || resolved == deployKey {
		return ""
	}
	return resolved
}

// projectLoadNote names a project-load fault in the refusal when there was one, so a broken
// reverse channel is not mistaken for "this project declares no box".
func projectLoadNote(err error) string {
	if err == nil {
		return ""
	}
	return fmt.Sprintf(" (the project could not be read: %v)", err)
}
