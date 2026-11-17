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
