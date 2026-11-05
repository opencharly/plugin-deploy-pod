package deploypod

import (
	"encoding/json"
	"testing"

	"github.com/opencharly/spec/spec"
)

// TestPrepareVenueState_DecodesIntoTheHostsWireType is the regression witness for the third
// image-crossing defect: the overlay ref a PrepareVenue reply carries must actually SURVIVE the
// host's decode. The assertion is deliberately end-to-end over the wire contract — marshal here,
// unmarshal into the exact type the host uses (spec.SaveDeployStateInput, whose consumer is
// deploykit.applyDeployState) — because the pre-fix code produced perfectly valid JSON that
// decoded to an empty struct, so any test that stopped at "the blob is non-empty" would have
// passed while the overlay went unpersisted.
func TestPrepareVenueState_DecodesIntoTheHostsWireType(t *testing.T) {
	const (
		overlay = "check-stepkind-emit-pod-overlay:0fafbf994885"
		base    = "ghcr.io/opencharly/check-pod:2026.216.2119"
	)
	state := prepareVenueState(overlay, base, "", nil)
	if len(state) == 0 {
		t.Fatal("prepareVenueState returned no state for a built overlay")
	}
	var in spec.SaveDeployStateInput
	if err := json.Unmarshal(state, &in); err != nil {
		t.Fatalf("host decode of the state blob: %v", err)
	}
	if in.ResolvedImage != overlay {
		t.Fatalf("decoded ResolvedImage = %q, want %q — the state blob does not use the wire key the host decodes (`resolved_image`), so the overlay ref is silently dropped and the deploy runs the BASE image", in.ResolvedImage, overlay)
	}

	// The pre-fix shape, kept as an executable statement of WHY: a hand-built map keyed on the Go
	// FIELD name marshals fine and decodes to nothing.
	handBuilt, err := json.Marshal(map[string]any{"ResolvedImage": overlay})
	if err != nil {
		t.Fatalf("marshal hand-built map: %v", err)
	}
	var stale spec.SaveDeployStateInput
	if err := json.Unmarshal(handBuilt, &stale); err != nil {
		t.Fatalf("decode hand-built map: %v", err)
	}
	if stale.ResolvedImage != "" {
		t.Fatalf("the Go-field-name key %s now decodes to %q; if the wire contract gained that alias this test's premise is stale — re-check prepareVenueState's doc comment", handBuilt, stale.ResolvedImage)
	}
}

// TestPrepareVenueState_NoOverlayWritesNothing: a deploy with no add_candy overlay and no
// engine selector resolves to the base image itself, and must persist NOTHING — otherwise the
// base ref would be pinned into the overlay slot and survive later rebuilds. A box name alone
// must not create an entry either.
func TestPrepareVenueState_NoOverlayWritesNothing(t *testing.T) {
	const base = "ghcr.io/opencharly/check-pod:2026.216.2119"
	if got := prepareVenueState(base, base, "", nil); got != nil {
		t.Fatalf("prepareVenueState(base, base, \"\", nil) = %s, want nil", got)
	}
	if got := prepareVenueState("", base, "", nil); got != nil {
		t.Fatalf("prepareVenueState(\"\", base, \"\", nil) = %s, want nil", got)
	}
	// The negative control for TestPrepareVenueState_CarriesEngine: a node that selected no
	// engine must keep the old "nothing to persist" outcome even when the box name is known.
	if got := prepareVenueState(base, base, "probe-engine-image", &spec.Deploy{}); got != nil {
		t.Fatalf("prepareVenueState(base, base, box, engine-less node) = %s, want nil", got)
	}
}

// TestPrepareVenueState_CarriesEngine is the regression witness for opencharly/charly#1109:
// a pod deploy's authored `engine:` selector must reach the per-host overlay even when no
// add_candy overlay was built. The host's config/start path resolves its run mode from that
// entry, so a selector dropped here is silently replaced by the host-wide engine.run and
// `charly config` emits a podman quadlet for an `engine: containerd` deploy. Fails on the
// pre-fix helper, which returned nil whenever resolvedImage == baseImage.
func TestPrepareVenueState_CarriesEngine(t *testing.T) {
	const base = "ghcr.io/opencharly/check-pod:2026.216.2119"
	state := prepareVenueState(base, base, "probe-engine-image", &spec.Deploy{Engine: spec.EngineName("containerd")})
	if len(state) == 0 {
		t.Fatal("prepareVenueState dropped the authored engine for a no-overlay deploy")
	}
	var in spec.SaveDeployStateInput
	if err := json.Unmarshal(state, &in); err != nil {
		t.Fatalf("host decode of the state blob: %v", err)
	}
	if in.Engine != spec.EngineName("containerd") {
		t.Fatalf("decoded Engine = %q, want containerd — the state blob does not carry the authored selector", in.Engine)
	}
	if in.ResolvedImage != "" {
		t.Fatalf("decoded ResolvedImage = %q for a no-overlay deploy; the base ref must not be pinned into the overlay slot", in.ResolvedImage)
	}
	// The entry must be a complete image-backed pod entry: the engine-only patch names the box
	// and the substrate so the node-form writer emits `pod:` (never a targetless `group:` that
	// would reject `engine` on the next load).
	if in.Box != "probe-engine-image" {
		t.Fatalf("decoded Box = %q, want probe-engine-image", in.Box)
	}
	if in.Target != "pod" {
		t.Fatalf("decoded Target = %q, want pod — an engine-only patch without a target marshals as a `group:` entry", in.Target)
	}
}

// TestPrepareVenueState_CarriesEngineAndOverlay proves the fields travel together when a
// real overlay was built: the engine is not traded for the resolved image, and the entry
// carries the box name.
func TestPrepareVenueState_CarriesEngineAndOverlay(t *testing.T) {
	const (
		overlay = "probe-overlay:0fafbf994885"
		base    = "ghcr.io/opencharly/pod-crabbox:2026.309.1054"
	)
	state := prepareVenueState(overlay, base, "probe-engine-image", &spec.Deploy{Engine: spec.EngineName("containerd")})
	var in spec.SaveDeployStateInput
	if err := json.Unmarshal(state, &in); err != nil {
		t.Fatalf("host decode of the state blob: %v", err)
	}
	if in.Engine != spec.EngineName("containerd") || in.ResolvedImage != overlay || in.Box != "probe-engine-image" {
		t.Fatalf("state = %+v, want engine=containerd, resolved_image=%s, box=probe-engine-image", in, overlay)
	}
}
