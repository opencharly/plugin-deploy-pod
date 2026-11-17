package deploypod

// secret_state_leak_test.go — D-SEC-1's `state` limb, asserted where it can actually fail.
//
// DESIGN.md:2511 forbids a secret entering "the store, artifacts, labels, the IR, state or output".
// `state` is listed SEPARATELY from the IR: a container's unit file is state, and the config writer
// persists it. The ONLY shape a unit may carry is the podman-secret REFERENCE —
// `Secret=<name>,type=env,target=<VAR>` — because podman resolves the value at REALIZE. The unit then
// holds a NAME; the bytes reach the process without ever being written down.
//
// WHY THIS FILE IS WRITTEN THE WAY IT IS. Its first version built its own
// `envVars := []string{"EXISTING=kept"}` and asserted the probe value was absent from the rendered
// text. That assertion could NEVER fail: the probe value is consumed by `CollectCandySecretAccepts`
// (which returns references) and by `EnsureBoxChainSecrets` (which returns names), so it never
// reached the hand-built slice. A test whose subject is a literal the test itself wrote is not a
// test of the code — it is a test of the literal. The review caught it, and the pasted red-first did
// not correspond to the committed test, which is the sharper half of the same finding.
//
// So the assertion is now on the PROPERTY that keeps the value out, and that property is the return
// SHAPE: `EnsureBoxChainSecrets` returns secret NAMES. A name never contains `=`; a `KEY=VALUE` pair
// does. Put a pair back and this fails.

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/opencharly/sdk/deploykit"
	"github.com/opencharly/spec/spec"
)

const leakProbeValue = "p75-state-leak-probe-9f2c"

// leakFixture is a one-level box whose candy authors the secret — the shape seam-proof-box takes.
func leakFixture() *spec.UnifiedFile {
	return &spec.UnifiedFile{
		Box: spec.BoxMap{"seam-proof-box": json.RawMessage(`{"candy":["seam-proof-candy"]}`)},
		Candy: map[string]json.RawMessage{
			"seam-proof-candy": json.RawMessage(`{"description":"declares one required secret","secret_require":[{"name":"SEAM_PROOF_TOKEN","key":"charly/seam-proof/token"}]}`),
		},
	}
}

// leakCred resolves the fixture's secret to the probe value, so a value-carrying path is visible.
func leakCred() deploykit.CredentialAccess {
	return deploykit.CredentialAccess{
		Resolve: func(envVar, service, key, defaultVal string) (string, string) {
			return leakProbeValue, "config"
		},
		Write: func(service, key, value string) error { return nil },
	}
}

// TestEnsureBoxChainSecretsReturnsNamesNotPairs is the absence assertion, on the return SHAPE.
//
// The value IS available to the resolver and IS resolved — the ensure must reach it to know the
// secret exists. What it must not do is hand it BACK to the caller, because the caller is the config
// writer and the writer's only destination for a pair is the unit's `Environment=`.
func TestEnsureBoxChainSecretsReturnsNamesNotPairs(t *testing.T) {
	ensured := EnsureBoxChainSecrets(leakFixture(), nil, "seam-proof-box", "", leakCred())

	if len(ensured) != 1 {
		t.Fatalf("the box chain's one required secret must be ensured by name, got %#v", ensured)
	}
	if ensured[0] != "SEAM_PROOF_TOKEN" {
		t.Fatalf("the ensure must return the secret's NAME, got %q", ensured[0])
	}
	// A name is a name. A `KEY=VALUE` pair is what the unit's Environment= line consumes — the shape
	// the previous version wrote, and the shape this change removed.
	if strings.Contains(ensured[0], "=") {
		t.Fatalf("the ensure returned an env PAIR, not a name: %q — D-SEC-1 forbids the value entering `state`", ensured[0])
	}
	if strings.Contains(strings.Join(ensured, " "), leakProbeValue) {
		t.Fatalf("the ensure returned the secret's VALUE in its result — D-SEC-1 forbids `state` carrying it")
	}
}

// TestTheUnitEnvironmentCarriesNoSecretPair is the direct regression on the exact line #76 wrote.
//
// It builds the Config's Env the way the config writer builds it — from the ensure's RESULT — and
// renders the unit. If the ensure ever returns a pair again, the unit's Environment= line carries
// the value and this fails. THIS is the assertion whose red-first the commit quotes.
func TestTheUnitEnvironmentCarriesNoSecretPair(t *testing.T) {
	ensured := EnsureBoxChainSecrets(leakFixture(), nil, "seam-proof-box", "", leakCred())

	// What the config writer does with whatever the ensure hands back.
	envFromEnsure := append([]string{"EXISTING=kept"}, ensured...)

	text := deploykit.GenerateQuadlet(deploykit.QuadletConfig{
		BoxName: "check-seam-proof", ImageRef: "seam-proof-box", Status: "working",
		UID: 1000, GID: 1000, Env: envFromEnsure,
	})

	if strings.Contains(text, "Environment=SEAM_PROOF_TOKEN=") {
		t.Fatalf("the unit's Environment= carries the secret — the ensure returned a pair:\n%s", text)
	}
	if strings.Contains(text, leakProbeValue) {
		t.Fatalf("the unit carries the secret's VALUE — D-SEC-1 forbids it entering `state`:\n%s", text)
	}
}

// TestTheRenderedUnitCarriesTheSecretReferenceAndNoValue is the artifact-level half: the reference
// list the image's label supplies renders as `Secret=…`, so delivery still has its carrier.
func TestTheRenderedUnitCarriesTheSecretReferenceAndNoValue(t *testing.T) {
	cred := leakCred()
	meta := &spec.BoxMetadata{SecretRequire: []spec.EnvDependency{{Name: "SEAM_PROOF_TOKEN"}}}

	refs, _ := deploykit.CollectCandySecretAccepts("seam-proof-box", "", meta, "charly/vnc", cred)
	if len(refs) != 1 {
		t.Fatalf("fixture: %d secret references collected, want 1", len(refs))
	}

	text := deploykit.GenerateQuadlet(deploykit.QuadletConfig{
		BoxName: "check-seam-proof", ImageRef: "seam-proof-box", Status: "working",
		UID: 1000, GID: 1000, Env: []string{"EXISTING=kept"}, Secrets: refs,
	})

	want := "Secret=" + refs[0].Name + ",type=env,target=SEAM_PROOF_TOKEN"
	if !strings.Contains(text, want) {
		t.Fatalf("the rendered unit carries no secret reference %q, so the value could not arrive at realize:\n%s", want, text)
	}
	if strings.Contains(text, leakProbeValue) {
		t.Fatalf("the rendered unit carries the secret's VALUE — D-SEC-1 forbids it entering `state`:\n%s", text)
	}
}
