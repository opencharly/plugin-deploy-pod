package deploypod

import (
	"context"
	"testing"

	"github.com/opencharly/sdk"
	"github.com/opencharly/sdk/deploykit"
	"github.com/opencharly/spec/spec"
)

// TestResolveBaseBoxName_QualifiedDeployKeyResolvesProjectImage is the R7 regression for
// opencharly/plugin-build#46.
//
// A qualified pod bed (`charly.check-sidecar-pod`) is dispatched as the deploy KEY
// `charly/check-sidecar-pod` (spec.DeployKey(ns, rest)). The host could not resolve that slash
// key against the project's namespace-qualified deploy map, so it synthesized a target-only node
// and podPrepareVenue received an EMPTY p.Image. The pre-fix fallback then handed the deploy KEY
// to `build:generate resolve` as a BOX name and failed with
// `unknown box "charly/check-sidecar-pod"`.
//
// The project projection (spec.UnifiedFile.Deploys) keys a namespaced deploy as `ns.name`, so the
// deploy KEY and the project key differ by exactly one separator. resolveBaseBoxName is the ONE
// place that reconciles them, shared by podPrepareVenue and podRebuild.
//
// LAYER PROVEN: the resolver helper. podPrepareVenue itself needs a live reverse-channel executor
// (HostBuild "overlay" + InvokeProvider) and is covered by the live `charly.check-sidecar-pod`
// bed, not by this unit test.
func TestResolveBaseBoxName_QualifiedDeployKeyResolvesProjectImage(t *testing.T) {
	// stubNoPerHost makes the per-host overlay read return nothing, so the assertions below
	// exercise the PROJECT fallback (a fresh `deploy add` has no per-host entry yet).
	stubNoPerHost := func(t *testing.T) {
		t.Helper()
		origLoadDeploy := loadDeploy
		t.Cleanup(func() { loadDeploy = origLoadDeploy })
		loadDeploy = func(_ context.Context, _ *sdk.Executor, _ string) (*deploykit.DeployConfig, error) {
			return nil, nil
		}
	}
	stubProject := func(t *testing.T, project *spec.UnifiedFile) {
		t.Helper()
		origConnect, origLoad := projectConnect, loadUnifiedForBox
		t.Cleanup(func() { projectConnect, loadUnifiedForBox = origConnect, origLoad })
		projectConnect = func(_ context.Context, _ *sdk.Executor, _ string, pre *spec.DeployPluginsConnectReply) error {
			pre.Dir = "/project"
			return nil
		}
		loadUnifiedForBox = func(_ context.Context, _ *sdk.Executor, _ string) (*spec.UnifiedFile, bool, error) {
			return project, true, nil
		}
	}

	// The umbrella's shape: the `charly` namespace imports the charly repo, whose charly.yml
	// declares the bed as `check-sidecar-pod: {pod: {image: check-k8s-deploy-app}}`.
	namespaced := &spec.UnifiedFile{
		Deploy: map[string]spec.DeployNode{},
		Namespaces: map[string]*spec.UnifiedFile{
			"charly": {
				Deploy: map[string]spec.DeployNode{
					"check-sidecar-pod": {Target: "pod", Image: "check-k8s-deploy-app"},
				},
			},
		},
	}

	t.Run("namespaced deploy key resolves the project image", func(t *testing.T) {
		stubNoPerHost(t)
		stubProject(t, namespaced)
		got := resolveBaseBoxName(context.Background(), nil, "charly/check-sidecar-pod", "", "test")
		if got != "check-k8s-deploy-app" {
			t.Fatalf("resolveBaseBoxName(charly/check-sidecar-pod) = %q, want %q — the deploy KEY must never reach the box namespace (opencharly/plugin-build#46)", got, "check-k8s-deploy-app")
		}
	})

	// The supporting mechanism: splitting the key into (base, instance) and handing those to the
	// resolver does NOT find the project entry, because the project key is `charly.check-sidecar-pod`
	// — not the bare first segment `charly`. This is why the fix must try the namespace-qualified
	// spelling rather than merely ParseDeployKey-ing the name.
	t.Run("splitting the key at the slash is not enough", func(t *testing.T) {
		stubNoPerHost(t)
		stubProject(t, namespaced)
		if got := deployKeyToBoxLocal(context.Background(), nil, nil, "charly", "check-sidecar-pod"); got != "" {
			t.Fatalf("deployKeyToBoxLocal(charly, check-sidecar-pod) = %q, want %q — this assertion documents why the slash split alone does not resolve the project entry", got, "")
		}
	})

	t.Run("per-host overlay entry wins without a project read", func(t *testing.T) {
		origLoadDeploy := loadDeploy
		origConnect := projectConnect
		t.Cleanup(func() { loadDeploy, projectConnect = origLoadDeploy, origConnect })
		loadDeploy = func(_ context.Context, _ *sdk.Executor, _ string) (*deploykit.DeployConfig, error) {
			return &deploykit.DeployConfig{Deploy: map[string]deploykit.DeployNode{
				"charly/check-sidecar-pod": {Target: "pod", Image: "check-k8s-deploy-app"},
			}}, nil
		}
		projectConnect = func(_ context.Context, _ *sdk.Executor, _ string, _ *spec.DeployPluginsConnectReply) error {
			t.Fatalf("a resolved per-host entry must not fall through to the project read")
			return nil
		}
		got := resolveBaseBoxName(context.Background(), nil, "charly/check-sidecar-pod", "", "test")
		if got != "check-k8s-deploy-app" {
			t.Fatalf("resolveBaseBoxName from the per-host overlay = %q, want %q", got, "check-k8s-deploy-app")
		}
	})

	t.Run("explicit image wins untouched", func(t *testing.T) {
		origLoadDeploy := loadDeploy
		t.Cleanup(func() { loadDeploy = origLoadDeploy })
		loadDeploy = func(_ context.Context, _ *sdk.Executor, _ string) (*deploykit.DeployConfig, error) {
			t.Fatalf("an explicit image must not consult the deploy resolver")
			return nil, nil
		}
		got := resolveBaseBoxName(context.Background(), nil, "charly/check-sidecar-pod", "explicit-ref:tag", "test")
		if got != "explicit-ref:tag" {
			t.Fatalf("resolveBaseBoxName with explicit image = %q, want %q", got, "explicit-ref:tag")
		}
	})

	t.Run("genuinely unresolvable key falls back to the key", func(t *testing.T) {
		stubNoPerHost(t)
		stubProject(t, &spec.UnifiedFile{Deploy: map[string]spec.DeployNode{}})
		got := resolveBaseBoxName(context.Background(), nil, "no-such-deploy", "", "test")
		if got != "no-such-deploy" {
			t.Fatalf("resolveBaseBoxName(unresolvable) = %q, want the key itself %q", got, "no-such-deploy")
		}
	})

	// A plain Pattern-A instance deploy is authored under its SLASH key in the project map
	// (`versa/ecovoyage` stays `versa/ecovoyage`; only namespace imports are dot-qualified), so the
	// exact key must be tried before the namespace-qualified spelling.
	t.Run("local instance deploy resolves under its slash key", func(t *testing.T) {
		stubNoPerHost(t)
		stubProject(t, &spec.UnifiedFile{Deploy: map[string]spec.DeployNode{
			"versa/ecovoyage": {Target: "pod", Image: "versa"},
		}})
		got := resolveBaseBoxName(context.Background(), nil, "versa/ecovoyage", "", "test")
		if got != "versa" {
			t.Fatalf("resolveBaseBoxName(versa/ecovoyage) = %q, want %q", got, "versa")
		}
	})
}
