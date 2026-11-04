package deploypod

import (
	"context"
	"testing"

	"github.com/opencharly/sdk"
	"github.com/opencharly/sdk/deploykit"
	"github.com/opencharly/spec/spec"
)

// TestResolvedOverlayImage guards the add_candy-on-pod deploy-resolution behavior the former
// core TestHostBuildPodConfigResolveRef_PrefersPersistedOverlay covered (relocated here with the
// #55 Cone A Unit 2 "pod-config-resolve-ref" seam-collapse): PrepareVenue persists the concrete
// overlay ref (DeployNode.ResolvedImage), and resolveDeployRefLocal must deploy THAT exact overlay
// (gated on it existing locally) instead of re-resolving the base image short-name (which a CalVer
// sort lets the base win on a same-minute build). resolvedOverlayImage is the pure extractor; the
// full base-name-vs-overlay preference in resolveDeployRefLocal (loadDeploy seam + LocalImageExists
// gate) is exercised live by the check-pod-overlay bed's R10.
func TestResolvedOverlayImage(t *testing.T) {
	const overlayRef = "check-addcandy-pod-overlay:abc123"
	cases := []struct {
		name      string
		deploy    map[string]deploykit.DeployNode
		box, inst string
		want      string
	}{
		{
			name:   "deploy-key entry wins",
			deploy: map[string]deploykit.DeployNode{spec.DeployKey("check-addcandy-pod", "work"): {ResolvedImage: overlayRef}},
			box:    "check-addcandy-pod", inst: "work", want: overlayRef,
		},
		{
			name:   "bare key (no instance)",
			deploy: map[string]deploykit.DeployNode{"check-addcandy-pod": {ResolvedImage: overlayRef}},
			box:    "check-addcandy-pod", inst: "", want: overlayRef,
		},
		{
			name:   "bare-key fallback when instance entry lacks resolved_image",
			deploy: map[string]deploykit.DeployNode{"check-addcandy-pod": {ResolvedImage: overlayRef}},
			box:    "check-addcandy-pod", inst: "work", want: overlayRef,
		},
		{
			name:   "no resolved_image → empty (base-name resolution used)",
			deploy: map[string]deploykit.DeployNode{"check-addcandy-pod": {Image: "check-pod"}},
			box:    "check-addcandy-pod", inst: "", want: "",
		},
		{
			name:   "no entry → empty",
			deploy: map[string]deploykit.DeployNode{},
			box:    "check-addcandy-pod", inst: "", want: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dc := &deploykit.DeployConfig{Deploy: tc.deploy}
			if got := resolvedOverlayImage(dc, tc.box, tc.inst); got != tc.want {
				t.Fatalf("resolvedOverlayImage = %q, want %q", got, tc.want)
			}
		})
	}
	if got := resolvedOverlayImage(nil, "x", ""); got != "" {
		t.Fatalf("resolvedOverlayImage(nil) = %q, want empty", got)
	}
}

// TestResolveDeployRefLocal_ExplicitRefShortCircuit proves the explicit_ref path (set only by
// `charly deploy from-box`) short-circuits both outputs BEFORE any reverse-channel load — so a nil
// executor is safe, exactly as the former host seam's explicit-ref-wins contract required.
func TestResolveDeployRefLocal_ExplicitRefShortCircuit(t *testing.T) {
	const ref = "ghcr.io/opencharly/versa:2026.211.0000"
	box, img, err := resolveDeployRefLocal(t.Context(), nil, "versa-prod", "", "sometag", ref)
	if err != nil {
		t.Fatalf("resolveDeployRefLocal explicit-ref: %v", err)
	}
	if box != ref || img != ref {
		t.Fatalf("explicit-ref short-circuit = (%q, %q), want (%q, %q)", box, img, ref, ref)
	}
}

// TestDeployKeyToBoxLocal_NamespaceQualifiedProjectFallback is the R7 regression for the
// namespaced-deploy resolution defect (RCA-H). On an empty per-host overlay, a namespaced
// pod bed (`charly.check-dsh-pod`) resolves its box ONLY through the project fallback
// (`loadUnifiedForBox` → `deploykit.ProjectDeployConfig(uf).Deploy[box]`). That projection
// must fold namespaces (`uf.Deploys()`), so the qualified key carries the LEAF box name.
//
// The pre-fix SDK pinned by this plugin (v0.2026266.623) projected root-scope `uf.Deploy`
// only, so `pc.Deploy["charly.check-dsh-pod"]` was ABSENT: the box name degraded to the
// deploy key, and `charly config` reported
// `image not found in local storage: charly.check-dsh-pod:charly.check-dsh-pod-<calver>`.
// This test FAILS against that pin (empty return) and PASSES with the namespace-aware SDK.
func TestDeployKeyToBoxLocal_NamespaceQualifiedProjectFallback(t *testing.T) {
	origConnect, origLoad := projectConnect, loadUnifiedForBox
	t.Cleanup(func() { projectConnect, loadUnifiedForBox = origConnect, origLoad })

	projectConnect = func(_ context.Context, _ *sdk.Executor, _ string, pre *spec.DeployPluginsConnectReply) error {
		pre.Dir = "/project"
		return nil
	}
	ns := &spec.UnifiedFile{Deploy: map[string]spec.DeployNode{
		"check-dsh-pod": {Target: "pod", Image: "dsh-app"},
	}}
	project := &spec.UnifiedFile{
		Deploy:     map[string]spec.DeployNode{},
		Namespaces: map[string]*spec.UnifiedFile{"charly": ns},
	}
	loadUnifiedForBox = func(_ context.Context, _ *sdk.Executor, _ string) (*spec.UnifiedFile, bool, error) {
		return project, true, nil
	}

	got := deployKeyToBoxLocal(context.Background(), nil, nil, "charly.check-dsh-pod", "")
	if got != "dsh-app" {
		t.Fatalf("deployKeyToBoxLocal(namespaced) = %q, want %q — the project fallback must fold namespaces (SDK >= v0.2026269.920)", got, "dsh-app")
	}
}
