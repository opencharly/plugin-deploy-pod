package deploypod

// The unit's real value is the GUARD, not the seam: `charly config` must never mint a credential for
// a candy this deploy does not run. `EnsureCandySecret` generates and persists a 32-byte token for a
// required dep that is missing everywhere, so an unscoped resolve writes foreign secrets into the
// operator's store — invisibly.

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

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

// containsSecretName reports whether the ensure returned `name`. The seam hands back NAMES, so this
// is the whole of what a caller can observe — there is no value to assert on, by design.
func containsSecretName(names []string, name string) bool {
	for _, n := range names {
		if n == name {
			return true
		}
	}
	return false
}

// The walk: box → base → roots, first-occurrence-wins, and NO foreign name.
func TestBoxCandyChainWalksBaseAndFrom(t *testing.T) {
	if got, want := boxCandyChain(chainFixture(), "app-box"), []string{"svc", "shared", "kernel"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("chain = %v, want %v (box → base, first occurrence wins)", got, want)
	}
}

// THE GUARD: the box's own required secret is ensured, and the foreign candy's is NOT minted.
func TestEnsureBoxChainSecretsMintsNothingOutsideTheBoxChain(t *testing.T) {
	uf, raw := chainFixture(), candiesFixture()
	chain := boxCandyChain(uf, "app-box")
	if len(chain) == 0 {
		t.Fatal("fixture: the chain is empty, so the guard below would prove nothing")
	}
	minted := map[string]string{}
	got := ensureBoxChainSecrets(raw, chain, mintRecorder(minted))

	if !containsSecretName(got, "SVC_TOKEN") {
		t.Fatalf("the box's own required secret was not ensured: got %v — the seam exists to ensure it", got)
	}
	if _, bad := minted["FOREIGN_TOKEN"]; bad {
		t.Fatalf("a candy OUTSIDE the box chain minted a credential (key FOREIGN_TOKEN, value %q): an unscoped resolve writes secrets into the operator's store that no deploy asked for", minted["FOREIGN_TOKEN"])
	}
	if containsSecretName(got, "FOREIGN_TOKEN") {
		t.Fatalf("a candy outside the box chain contributed a name: %v", got)
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

// TestEnsureBoxChainSecretsEndToEnd is the test the reviewer asked for: a real authored
// `secret_require:` reaching the environment THROUGH THE CHAIN, with a foreign candy proving the
// scope. The earlier tests drove boxCandyChain and ensureBoxChainSecrets separately; this one runs
// the pair a config write actually runs, so deleting either link fails it.
//
// The chain: app-box (candy: [svc], base: base-box) -> base-box (candy: [shared]). Both svc and
// shared author a required secret; foreign is a third candy in NEITHER level and must contribute
// nothing — that is EnsureCandySecret's auto-generation not being reached.
//
// The credential accessor is a FAKE, because this asserts WHICH candies are resolved and HOW the
// value is carried, not that the operator's store works. R8 does not apply: no real service is
// crossed here, and the store is not mocked as a service — it is a two-function seam whose real
// implementation is tested by sdk.
func TestEnsureBoxChainSecretsEndToEnd(t *testing.T) {
	uf := &spec.UnifiedFile{
		Box: spec.BoxMap{
			"app-box":  json.RawMessage(`{"candy":["svc"],"base":"base-box"}`),
			"base-box": json.RawMessage(`{"candy":["shared"]}`),
		},
		Candy: map[string]json.RawMessage{
			"svc":     json.RawMessage(`{"secret_require":[{"name":"DB_PASSWORD","service":"db","key":"pw"}]}`),
			"shared":  json.RawMessage(`{"secret_require":[{"name":"SHARED_TOKEN","service":"x","key":"y"}]}`),
			"foreign": json.RawMessage(`{"secret_require":[{"name":"FOREIGN_TOKEN","service":"f","key":"k"}]}`),
		},
	}

	resolved := map[string]int{}
	cred := deploykit.CredentialAccess{
		Resolve: func(envVar, service, key, defaultVal string) (string, string) {
			resolved[envVar]++
			return "value-of-" + key, "store"
		},
		Write: func(service, key, value string) error {
			t.Fatalf("no candy in this chain may MINT a credential; got Write(%q,%q)", service, key)
			return nil
		},
	}

	got := ensureBoxChainSecrets(uf.Candy, boxCandyChain(uf, "app-box"), cred)

	// 1. Both chain levels are ENSURED, by NAME. The seam returns NAMES only (D-SEC-1: a value must
	// reach neither `state` nor the caller), so the value's absence is not this test's to assert --
	// TestTheRenderedUnitCarriesTheSecretReferenceAndNoValue owns that half.
	if !containsSecretName(got, "DB_PASSWORD") {
		t.Fatalf("the leaf box's candy must ensure DB_PASSWORD, got %#v", got)
	}
	if !containsSecretName(got, "SHARED_TOKEN") {
		t.Fatalf("the BASE box's candy must ensure SHARED_TOKEN, got %#v", got)
	}

	// 2. The scope holds: the foreign candy is in neither level, so nothing was read for it.
	if containsSecretName(got, "FOREIGN_TOKEN") {
		t.Fatalf("a candy outside the box chain must contribute nothing, got %#v", got)
	}
	if resolved["FOREIGN_TOKEN"] != 0 {
		t.Fatalf("a foreign candy must not even be LOOKED UP, got %d reads", resolved["FOREIGN_TOKEN"])
	}

	// 3. And exactly two reads happened: one per authored dep, no more.
	if total := resolved["DB_PASSWORD"] + resolved["SHARED_TOKEN"]; total != 2 {
		t.Fatalf("expected exactly 2 credential reads, got %d (%#v)", total, resolved)
	}
}

// TestEnsureBoxChainSecretsNamesTheChainsSecrets asserts the seam's OUTPUT: the NAMES it ensured, and
// never a value. It replaced a test that asserted the value's presence in the env slice — which was
// the D-SEC-1 `state` leak, not the property (the unit's `Environment=` line is state).
func TestEnsureBoxChainSecretsNamesTheChainsSecrets(t *testing.T) {
	uf := &spec.UnifiedFile{
		Box: spec.BoxMap{
			"app-box":  json.RawMessage(`{"candy":["svc"],"base":"base-box"}`),
			"base-box": json.RawMessage(`{"candy":["shared"]}`),
		},
		Candy: map[string]json.RawMessage{
			"svc":     json.RawMessage(`{"secret_require":[{"name":"DB_PASSWORD","service":"db","key":"pw"}]}`),
			"shared":  json.RawMessage(`{"secret_require":[{"name":"SHARED_TOKEN","service":"x","key":"y"}]}`),
			"foreign": json.RawMessage(`{"secret_require":[{"name":"FOREIGN_TOKEN","service":"f","key":"k"}]}`),
		},
	}
	cred := deploykit.CredentialAccess{
		Resolve: func(envVar, service, key, defaultVal string) (string, string) {
			return "v-" + envVar, "store"
		},
		Write: func(service, key, value string) error { return nil },
	}

	got := EnsureBoxChainSecrets(uf, nil, "app-box", "", cred)

	for _, want := range []string{"DB_PASSWORD", "SHARED_TOKEN"} {
		if !containsSecretName(got, want) {
			t.Fatalf("the box chain's %s was not ensured; got %#v", want, got)
		}
	}
	if containsSecretName(got, "FOREIGN_TOKEN") {
		t.Fatalf("a candy outside the box chain must not be ensured; got %#v", got)
	}
	// The seam's whole point: it hands back NAMES, so a caller has no value to write into state.
	for _, n := range got {
		if strings.Contains(n, "=") || strings.Contains(n, "v-") {
			t.Fatalf("the seam returned a VALUE (%q); D-SEC-1 forbids one leaving the store here", n)
		}
	}
}

// TestConfigSetupCallsTheSecretEnsure is the CALL-SITE test, and it exists because a behavioural test
// cannot reach it: the ensure is correct in isolation whether or not runConfig calls it, and runConfig
// needs an Executor, a ResolvedRuntime and a podSelection to drive.
//
// It is asserted where it lives -- in the source -- and labelled as the weaker proof it is: it fails
// if the call is DELETED. It also pins the ORDER (before provisioning) and the ABSENCE of the retired
// env write, since either regression silently reverts the D-SEC-1 `state` fix.
func TestConfigSetupCallsTheSecretEnsure(t *testing.T) {
	src, err := os.ReadFile("config_setup.go")
	if err != nil {
		t.Fatalf("cannot read the config path source: %v", err)
	}
	body := string(src)
	ensureAt := strings.Index(body, "EnsureBoxChainSecrets(")
	if ensureAt < 0 {
		t.Fatal("config_setup.go no longer calls EnsureBoxChainSecrets: a required secret missing " +
			"everywhere would fail the config instead of being minted, and every behavioural test " +
			"would still pass -- precisely the gap this assertion exists to close")
	}
	provisionAt := strings.Index(body, "resolvePodProvisionSecrets(")
	if provisionAt < 0 {
		t.Fatal("config_setup.go no longer provisions the podman secrets: the unit's Secret= reference " +
			"would name a secret that does not exist")
	}
	if ensureAt > provisionAt {
		t.Fatal("EnsureBoxChainSecrets runs AFTER resolvePodProvisionSecrets: a value minted on the " +
			"first `charly config` would not be the one provisioned, so the container gets no secret")
	}
	if strings.Contains(body, "PodEnvVarsWithSecrets(") {
		t.Fatal("config_setup.go still writes secret VALUES into the container's Env:, which is `state` " +
			"(D-SEC-1); the unit may carry only the podman-secret reference")
	}
	// And the loader gate must still guard it: an unloaded project ensures nothing, silently.
	if !strings.Contains(body, "loadUnifiedForBox(ctx, ex, c.Box)") {
		t.Fatal("config_setup.go no longer gates the ensure on loadUnifiedForBox: a failed load " +
			"would silently ensure no secrets instead of skipping the seam")
	}
}

// TestEnsureBoxChainSecretsCarriesAnAddCandySecret is D-CAP-6's own fixture, and it is the one a bed
// CANNOT give us: a candy that exists ONLY in the deploy's `add_candy:` overlay -- in neither box
// level -- must still be ensured, because "the needs and uses of a deployment are the union of those
// of the deployment, its box and ALL THEIR CANDIES" (DESIGN.md:695).
//
// The discriminating shape, learned the hard way: the overlay candy must be reachable ONLY through
// add_candy. If it were also in the box chain, both implementations would agree and the fixture could
// not see the bug -- the same reason three earlier precedence tests passed against inverted code.
func TestEnsureBoxChainSecretsCarriesAnAddCandySecret(t *testing.T) {
	uf := &spec.UnifiedFile{
		Box: spec.BoxMap{
			"app-box": json.RawMessage(`{"candy":["box-candy"]}`),
		},
		Candy: map[string]json.RawMessage{
			"box-candy": json.RawMessage(`{"secret_require":[{"name":"BOX_TOKEN","service":"b","key":"b"}]}`),
			"overlay":   json.RawMessage(`{"secret_require":[{"name":"OVERLAY_TOKEN","service":"o","key":"o"}]}`),
			"unrelated": json.RawMessage(`{"secret_require":[{"name":"UNRELATED_TOKEN","service":"u","key":"u"}]}`),
		},
	}
	// The deploy carries `overlay` via add_candy and nothing else.
	dc := &spec.DeployConfig{Deploy: map[string]spec.DeployNode{
		"app-box": {AddCandy: []spec.CandyRef{"overlay"}},
	}}
	cred := deploykit.CredentialAccess{
		Resolve: func(envVar, service, key, defaultVal string) (string, string) { return "v-" + envVar, "store" },
		Write:   func(service, key, value string) error { return nil },
	}

	got := EnsureBoxChainSecrets(uf, dc, "app-box", "", cred)

	if !containsSecretName(got, "BOX_TOKEN") {
		t.Fatalf("the box chain's candy must still be ensured; got %#v", got)
	}
	if !containsSecretName(got, "OVERLAY_TOKEN") {
		t.Fatalf("an `add_candy:` candy IS one of the deployment's candies (D-CAP-6) and must be ensured; got %#v", got)
	}
	if containsSecretName(got, "UNRELATED_TOKEN") {
		t.Fatalf("a candy in neither the box chain nor add_candy must contribute nothing; got %#v", got)
	}
}

// TestDeployCandyChainBoxFirstThenAddCandy pins the ORDER: where a candy appears in both, the box's
// reading wins and it appears once.
func TestDeployCandyChainBoxFirstThenAddCandy(t *testing.T) {
	uf := &spec.UnifiedFile{Box: spec.BoxMap{
		"app-box": json.RawMessage(`{"candy":["shared","box-only"]}`),
	}}
	got := DeployCandyChain(uf, "app-box", []string{"overlay", "shared", ""})
	want := []string{"shared", "box-only", "overlay"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("the union must be box-first, first-occurrence-wins, empties dropped:\n got %#v\nwant %#v", got, want)
	}
}

// TestBoxCandyChainTerminatesOnACycle is the loop's own proof, and it exists because the first
// version could hang `charly config`.
//
// The walk tracked only CANDY names, and its only cycle guard was `if next == base`, which catches
// the self-loop `A -> A`. A two-box cycle `A -> B -> A` revisits boxes whose candies are already
// collected, so the candy-level `seen` never fires and the loop spins forever. The gate for a
// malformed manifest is validation's; THIS function's obligation is to terminate.
func TestBoxCandyChainTerminatesOnACycle(t *testing.T) {
	uf := &spec.UnifiedFile{Box: spec.BoxMap{
		"a": json.RawMessage(`{"candy":["one"],"base":"b"}`),
		"b": json.RawMessage(`{"candy":["two"],"base":"a"}`),
	}}

	done := make(chan []string, 1)
	go func() { done <- boxCandyChain(uf, "a") }()
	select {
	case got := <-done:
		// Both boxes' candies are collected once each, then the walk stops.
		if len(got) != 2 {
			t.Fatalf("a cyclic chain must collect each box's candies once and stop, got %#v", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("boxCandyChain did not terminate on a cyclic chain: `charly config` would HANG")
	}
}
