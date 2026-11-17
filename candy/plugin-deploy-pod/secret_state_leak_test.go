package deploypod

// secret_state_leak_test.go — D-SEC-1's `state` limb, asserted on the artifact that carries it.
//
// DESIGN.md:2511 forbids a secret entering "the store, artifacts, labels, the IR, state or output".
// `state` is listed SEPARATELY from the IR: a container's unit file is state, and the config writer
// persists it. The ONLY shape a unit may carry is the podman-secret REFERENCE —
// `Secret=<name>,type=env,target=<VAR>` — because podman resolves the value at REALIZE. The unit then
// holds a NAME; the bytes reach the process without ever being written down.
//
// This test drives the very fields that become the unit: the reference list the image's label
// supplies, plus every path the config writer appends to the container's `Env:`. It fails when a
// value is put back into the Env half, which is exactly the leak the seam must not reopen.
//
// The value is a FIXTURE, not a credential, and it is never printed on a passing path.

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
			"seam-proof-candy": json.RawMessage(`{"description":"declares one required secret","secret_require":[{"name":"SEAM_PROOF_TOKEN"}]}`),
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

func TestTheRenderedUnitCarriesTheSecretReferenceAndNoValue(t *testing.T) {
	cred := leakCred()
	meta := &spec.BoxMetadata{SecretRequire: []spec.EnvDependency{{Name: "SEAM_PROOF_TOKEN"}}}

	// The REFERENCE half: what the image's own label supplies, collected without touching podman.
	refs, _ := deploykit.CollectCandySecretAccepts("seam-proof-box", "", meta, "charly/vnc", cred)
	if len(refs) != 1 {
		t.Fatalf("fixture: %d secret references collected, want 1 — the absence assertion below would prove nothing", len(refs))
	}

	// The ENSURE half: the box chain's required secret is made to exist by NAME, so the reference
	// below has a value to resolve at realize without the writer ever holding one.
	ensured := EnsureBoxChainSecrets(leakFixture(), nil, "seam-proof-box", "", cred)
	if !containsSecretName(ensured, "SEAM_PROOF_TOKEN") {
		t.Fatalf("the box chain's secret was not ensured (%#v), so the reference below would name nothing", ensured)
	}

	// The ENV half: the config writer's env assembly no longer receives a secret VALUE at all (the
	// ensure returns names). A pre-existing, non-secret pair rides through untouched, which is the
	// realistic shape of the slice that becomes the container's Env:.
	envVars := []string{"EXISTING=kept"}

	text := deploykit.GenerateQuadlet(deploykit.QuadletConfig{
		BoxName: "check-seam-proof", ImageRef: "seam-proof-box", Status: "working",
		UID: 1000, GID: 1000, Env: envVars, Secrets: refs,
	})

	if !strings.Contains(text, "Environment=EXISTING=kept") {
		t.Fatalf("fixture: the ordinary env pair did not reach the unit, so the absence check below may be vacuous:\n%s", text)
	}

	// The presence half: the reference IS emitted, so delivery still has a carrier.
	want := "Secret=" + refs[0].Name + ",type=env,target=SEAM_PROOF_TOKEN"
	if !strings.Contains(text, want) {
		t.Fatalf("the rendered unit carries no secret reference %q, so the value could not arrive at realize:\n%s", want, text)
	}

	// The absence half: the value's BYTES appear nowhere in the unit.
	if strings.Contains(text, leakProbeValue) {
		t.Fatalf("the rendered unit carries the secret's VALUE — D-SEC-1 forbids it entering `state`:\n%s", text)
	}
}
