package deploypod

import (
	"context"
	"testing"

	"github.com/opencharly/sdk"
	"github.com/opencharly/sdk/deploykit"
	"github.com/opencharly/sdk/kit"
	"github.com/opencharly/spec/spec"
)

// TestResolveDeployPorts_HonoursCLIPinsOnAFreshDeploy pins plugin-deploy-pod#52: the operator's
// `-p H:C` pins arrive on the FIRST config of a deploy, when the entry has no `port:` yet.
// Port resolution ran BEFORE the CLI overrides were merged, so the resolver saw no pin, the
// auto-allocation was persisted as `resolved_port`, and the quadlet published THAT — the pin
// was accepted, printed, and silently dropped. Re-running the identical command honoured it,
// which is what hid the bug.
func TestResolveDeployPorts_HonoursCLIPinsOnAFreshDeploy(t *testing.T) {
	saveStub := &saveDeployStub{}
	saveStub.install(t)
	fake := &fakeVolumeExecutorServiceClient{}
	ex := sdk.NewInProcExecutor(fake)
	meta := &spec.BoxMetadata{Port: []string{"8080"}}

	var dc *deploykit.DeployConfig // nil: a deploy's first-ever config
	key := spec.DeployKey("fresh-pin", "")

	if err := resolveDeployPorts(context.Background(), ex, &dc, key, meta, "18080:8080"); err != nil {
		t.Fatalf("resolveDeployPorts() error = %v", err)
	}
	entry, ok := dc.Deploy[key]
	if !ok || len(entry.ResolvedPort) != 1 {
		t.Fatalf("dc.Deploy[%q] = %+v, want exactly one resolved port", key, entry)
	}
	pm, ok := kit.ParsePortMapping(entry.ResolvedPort[0])
	if !ok || pm.Host != 18080 || pm.Container != 8080 {
		t.Fatalf("resolved port %q, want the CLI pin 18080:8080 — the pin must reach the FIRST resolution, not only a re-run", entry.ResolvedPort[0])
	}
}

// TestResolveDeployPorts_CLIPinWinsOverAuthorPin pins the precedence: the CLI pin is applied
// LAST (matching kit.ApplyPortOverrides), so it overrides an authored entry pin for the same
// container port.
func TestResolveDeployPorts_CLIPinWinsOverAuthorPin(t *testing.T) {
	saveStub := &saveDeployStub{}
	saveStub.install(t)
	fake := &fakeVolumeExecutorServiceClient{}
	ex := sdk.NewInProcExecutor(fake)
	meta := &spec.BoxMetadata{Port: []string{"8080"}}

	key := spec.DeployKey("author-vs-cli", "")
	dc := &deploykit.DeployConfig{Deploy: map[string]spec.DeployNode{
		key: {Target: "pod", Image: "demo", Port: []string{"19000:8080"}},
	}}

	if err := resolveDeployPorts(context.Background(), ex, &dc, key, meta, "18080:8080"); err != nil {
		t.Fatalf("resolveDeployPorts() error = %v", err)
	}
	entry := dc.Deploy[key]
	if len(entry.ResolvedPort) != 1 {
		t.Fatalf("want one resolved port, got %+v", entry.ResolvedPort)
	}
	pm, ok := kit.ParsePortMapping(entry.ResolvedPort[0])
	if !ok || pm.Host != 18080 {
		t.Fatalf("resolved port %q, want the CLI pin's host 18080 to win over the authored 19000", entry.ResolvedPort[0])
	}
}
