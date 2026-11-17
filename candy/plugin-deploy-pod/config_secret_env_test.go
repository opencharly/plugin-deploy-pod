package deploypod

// The unit's real value is the GUARD, not the seam: `charly config` must never mint a credential for
// a candy this deploy does not run. `EnsureCandySecret` generates and persists a 32-byte token for a
// required dep that is missing everywhere, so an unscoped resolve writes foreign secrets into the
// operator's store — invisibly.

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/opencharly/sdk/deploykit"
	"github.com/opencharly/spec/spec"
)

// chainFixture is a two-level box chain plus a candy that belongs to NEITHER level.
func chainFixture() *spec.UnifiedFile {
	return &spec.UnifiedFile{Box: spec.BoxMap{
		"app-box":  json.RawMessage(`{"from":"base-box","candy":["svc","shared"]}`),
		"base-box": json.RawMessage(`{"candy":["shared","kernel"]}`),
	}}
}

func candiesFixture() map[string]json.RawMessage {
	return map[string]json.RawMessage{
		"svc":     json.RawMessage(`{"description":"the deploy's own candy","secret_require":[{"name":"SVC_TOKEN"}]}`),
		"kernel":  json.RawMessage(`{"description":"an inherited candy"}`),
		"shared":  json.RawMessage(`{"description":"both levels compose this"}`),
		"foreign": json.RawMessage(`{"description":"declared in the project, NOT in this box chain","secret_require":[{"name":"FOREIGN_TOKEN"}]}`),
	}
}

// mintRecorder stubs the credential store: a resolve that finds nothing, and a write that records
// what would have been minted.
func mintRecorder(minted map[string]string) deploykit.CredentialAccess {
	return deploykit.CredentialAccess{
		Resolve: func(envVar, service, key, defaultVal string) (string, string) { return "", "" },
		Write:   func(service, key, value string) error { minted[key] = value; return nil },
	}
}

// The walk: box → base → roots, first-occurrence-wins, and NO foreign name.
func TestBoxCandyChainWalksBaseAndFrom(t *testing.T) {
	if got, want := boxCandyChain(chainFixture(), "app-box"), []string{"svc", "shared", "kernel"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("chain = %v, want %v (box → base, first occurrence wins)", got, want)
	}
}

// THE GUARD: the box's own required secret is resolved, and the foreign candy's is NOT minted.
func TestScopedCandySecretsMintsNothingOutsideTheBoxChain(t *testing.T) {
	uf, raw := chainFixture(), candiesFixture()
	chain := boxCandyChain(uf, "app-box")
	if len(chain) == 0 {
		t.Fatal("fixture: the chain is empty, so the guard below would prove nothing")
	}
	minted := map[string]string{}
	got := scopedCandySecrets(raw, chain, mintRecorder(minted))

	if got["SVC_TOKEN"] == "" {
		t.Fatalf("the box's own required secret was not resolved: got %v — the seam exists to deliver it", got)
	}
	if _, bad := minted["FOREIGN_TOKEN"]; bad {
		t.Fatalf("a candy OUTSIDE the box chain minted a credential (key FOREIGN_TOKEN, value %q): an unscoped resolve writes secrets into the operator's store that no deploy asked for", minted["FOREIGN_TOKEN"])
	}
	if _, bad := got["FOREIGN_TOKEN"]; bad {
		t.Fatalf("a candy outside the box chain contributed an environment value: %v", got)
	}
}

// TestDeployBoxBasePrefersTheDeploysBoxOverItsImage is the ENTRY-POINT test, and it exists because
// the first version got the precedence backwards: it took `node.Image` as the chain's start
// whenever it was set, so a REGISTRY deploy (`image: quay.io/fedora/fedora:43`) started the walk
// from a ref that names no box, found nothing, and silently delivered no secrets -- while its own
// c.Box named a perfectly good chain.
//
// The fixture's deploy KEYS are the box names, because that is how the lookup finds a node at all
// (deployBoxBase indexes dc.Deploy by DeployKey(box, instance)). An earlier version of this test
// used unrelated keys, so the lookup missed, every case fell through to `return box`, and the test
// passed against the INVERTED implementation it was written to catch. A fixture whose keys do not
// match cannot see the bug.
func TestDeployBoxBasePrefersTheDeploysBoxOverItsImage(t *testing.T) {
	uf := &spec.UnifiedFile{Box: spec.BoxMap{
		"real-box":  json.RawMessage(`{"candy":["svc"]}`),
		"image-box": json.RawMessage(`{"candy":["other"]}`),
	}}
	dc := &spec.DeployConfig{Deploy: map[string]spec.DeployNode{
		// BOTH are boxes, and they differ. That is the ONLY fixture that can distinguish
		// "prefers the deploy's box" from "prefers the image": when the image is not a box both
		// implementations fall through to `box` and agree, which is why two earlier versions of
		// this test passed against the INVERTED code they were written to catch.
		"real-box": {Image: "image-box"},
		// A registry image: not a box, so it can never be the walk's start either way.
		"registry-box": {Image: "quay.io/fedora/fedora:43"},
		// The box names nothing; the image does. This is the legitimate fallback.
		"unknown": {Image: "image-box"},
	}}

	// 1. THE DISCRIMINATOR: the deploy's own box wins over an image that is also a box.
	if got := deployBoxBase(uf, dc, "real-box", ""); got != "real-box" {
		t.Fatalf("the deploy's OWN box must win over an image that is ALSO a box, got %q", got)
	}

	// 2. A registry image names no box: it can never be the start.
	if got := deployBoxBase(uf, dc, "registry-box", ""); got != "registry-box" {
		t.Fatalf("a registry image must never be walked as a box, got %q", got)
	}

	// 3. A box that names nothing, with an image that IS a box: the image is the fallback.
	if got := deployBoxBase(uf, dc, "unknown", ""); got != "image-box" {
		t.Fatalf("an image that names a box is the fallback, got %q", got)
	}

	// 4. A nil deploy config must not panic and must return the box.
	if got := deployBoxBase(uf, nil, "real-box", ""); got != "real-box" {
		t.Fatalf("a nil deploy config must return the box, got %q", got)
	}
}

// TestBoxCandyChainFollowsBaseAndFrom is the walk itself: box -> base, first occurrence wins.
func TestBoxCandyChainFollowsBaseAndFrom(t *testing.T) {
	uf := &spec.UnifiedFile{Box: spec.BoxMap{
		"leaf": json.RawMessage(`{"candy":["svc","shared"],"base":"mid"}`),
		"mid":  json.RawMessage(`{"candy":["shared","kernel"],"from":"root"}`),
		"root": json.RawMessage(`{"candy":["base-candy"]}`),
	}}
	got := boxCandyChain(uf, "leaf")
	want := []string{"svc", "shared", "kernel", "base-candy"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("the chain must walk base then from, first occurrence winning:\n got %#v\nwant %#v", got, want)
	}
}
