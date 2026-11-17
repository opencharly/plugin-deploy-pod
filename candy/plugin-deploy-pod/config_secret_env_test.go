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

// TestScopedCandySecretsEndToEnd is the test the reviewer asked for: a real authored
// `secret_require:` reaching the environment THROUGH THE CHAIN, with a foreign candy proving the
// scope. The earlier tests drove boxCandyChain and scopedCandySecrets separately; this one runs
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
func TestScopedCandySecretsEndToEnd(t *testing.T) {
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

	got := scopedCandySecrets(uf.Candy, boxCandyChain(uf, "app-box"), cred)

	// 1. Both chain levels deliver, at the authored env-var name.
	//
	// The VALUE is what the fake returns for the argument it is handed, and that argument is the
	// dep's NAME -- not its authored key. Written as "value-of-"+key originally, this assertion
	// caught its own wrong assumption: the seam asks the store for (envVar, service, key) and
	// EnsureCandySecret owns that coordinate, so this file must not care which of the three the
	// store keys on. Asserting the value's identity is enough.
	if got["DB_PASSWORD"] != "value-of-DB_PASSWORD" {
		t.Fatalf("the leaf box's candy must deliver DB_PASSWORD, got %#v", got)
	}
	if got["SHARED_TOKEN"] != "value-of-SHARED_TOKEN" {
		t.Fatalf("the BASE box's candy must deliver SHARED_TOKEN, got %#v", got)
	}

	// 2. The scope holds: the foreign candy is in neither level, so nothing was read for it.
	if _, found := got["FOREIGN_TOKEN"]; found {
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

// TestPodEnvVarsCarriesTheChainsSecret is the test the reviewer asked for twice, and it asserts the
// EMITTED SLICE -- the exact []string that becomes the container's Env: -- rather than a helper the
// caller might not use. PodEnvVarsWithSecrets IS the wiring's body in config_setup.go, so removing
// that call removes the only thing this test drives.
func TestPodEnvVarsCarriesTheChainsSecret(t *testing.T) {
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

	// A pre-existing env pair must SURVIVE: the build appends, it does not replace.
	got := PodEnvVarsWithSecrets([]string{"EXISTING=kept"}, uf, nil, "app-box", "", cred)

	joined := strings.Join(got, "\n")
	if !strings.Contains(joined, "EXISTING=kept") {
		t.Fatalf("the build must APPEND, not replace; got %#v", got)
	}
	if !strings.Contains(joined, "DB_PASSWORD=v-DB_PASSWORD") {
		t.Fatalf("the emitted Env must carry the leaf candy's secret; got %#v", got)
	}
	if !strings.Contains(joined, "SHARED_TOKEN=v-SHARED_TOKEN") {
		t.Fatalf("the emitted Env must carry the BASE candy's secret; got %#v", got)
	}
	if strings.Contains(joined, "FOREIGN_TOKEN") {
		t.Fatalf("a candy outside the box chain must not reach Env; got %#v", got)
	}
}

// TestConfigSetupCallsTheSecretEnvBuild is the CALL-SITE test, and it exists because a behavioural
// test cannot reach it: PodEnvVarsWithSecrets is correct in isolation whether or not runConfig
// calls it, and runConfig needs an Executor, a ResolvedRuntime and a podSelection to drive.
//
// So the wiring is asserted where it lives -- in the source. This is a weaker proof than executing
// the path, and it is labelled as such: it fails if the call is DELETED, which is exactly the
// regression the reviewer named ("delete the 11 wiring lines and every unit test stays green"). It
// does not prove the call RUNS.
func TestConfigSetupCallsTheSecretEnvBuild(t *testing.T) {
	src, err := os.ReadFile("config_setup.go")
	if err != nil {
		t.Fatalf("cannot read the config path source: %v", err)
	}
	if !strings.Contains(string(src), "PodEnvVarsWithSecrets(") {
		t.Fatal("config_setup.go no longer calls PodEnvVarsWithSecrets: the config-time secret " +
			"delivery is unwired, and every behavioural test would still pass -- which is precisely " +
			"the gap this assertion exists to close")
	}
	// And the loader gate must still guard it: an unloaded project delivers nothing, silently.
	if !strings.Contains(string(src), "loadUnifiedForBox(ctx, ex, c.Box)") {
		t.Fatal("config_setup.go no longer gates the delivery on loadUnifiedForBox: a failed load " +
			"would silently deliver no secrets instead of skipping the seam")
	}
}
