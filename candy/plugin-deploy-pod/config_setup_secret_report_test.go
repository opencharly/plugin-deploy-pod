package deploypod

import (
	"strings"
	"testing"

	"github.com/opencharly/spec/spec"
)

// The tests here pin the ONE thing a `[config]` failure must tell an operator: WHY the credential was
// not resolved — an empty coordinate, or a store that could not be read — and WHICH coordinate was
// queried. Before opencharly/plugin-deploy-pod#61 both rendered as "Store them in the credential
// backend", which sent the operator in opencharly/sdk#526 to duplicate a credential they already had.

func dep(name, key string) spec.EnvDependency {
	return spec.EnvDependency{Name: name, Key: key}
}

// TestMissingSecretReport_Resolved proves a resolved deploy prints nothing and returns nil.
func TestMissingSecretReport_Resolved(t *testing.T) {
	report, err := missingSecretReport("box", []spec.EnvDependency{dep("TOK", "")},
		[]secretResolution{{Name: "TOK", Source: "keyring", Resolved: true, Required: true}})
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if report != "" {
		t.Errorf("report = %q, want empty", report)
	}
}

// TestMissingSecretReport_AbsentNamesTheCoordinate: a readable store with an empty coordinate keeps the
// store-it advice, and now says WHERE it looked — the default `charly/secret/<name>` when the candy
// declared no `key:`.
func TestMissingSecretReport_AbsentNamesTheCoordinate(t *testing.T) {
	report, err := missingSecretReport("mybox", []spec.EnvDependency{dep("WEBUI_ADMIN_PASSWORD", "")},
		[]secretResolution{{Name: "WEBUI_ADMIN_PASSWORD", Source: "default", Resolved: false, Required: true}})
	if err == nil {
		t.Fatal("err = nil, want the missing-secret error")
	}
	if !strings.Contains(err.Error(), "missing required credential-backed secret") {
		t.Errorf("err = %v, want the missing-secret class", err)
	}
	if !strings.Contains(report, "charly/secret/WEBUI_ADMIN_PASSWORD") {
		t.Errorf("report does not name the queried coordinate:\n%s", report)
	}
	if !strings.Contains(report, "Store them in the credential backend") {
		t.Errorf("report dropped the store-it advice for a readable store:\n%s", report)
	}
}

// TestMissingSecretReport_ExplicitKeyNamesThatCoordinate: a candy-declared `key:` is rendered as the
// (service, key) pair the resolver used — the SAME split, never a re-derived one.
func TestMissingSecretReport_ExplicitKeyNamesThatCoordinate(t *testing.T) {
	report, err := missingSecretReport("mybox", []spec.EnvDependency{dep("QDRANT__SERVICE__API_KEY", "charly/api-key/qdrant")},
		[]secretResolution{{Name: "QDRANT__SERVICE__API_KEY", Source: "default", Resolved: false, Required: true}})
	if err == nil {
		t.Fatal("err = nil, want the missing-secret error")
	}
	if !strings.Contains(report, "charly/api-key/qdrant") {
		t.Errorf("report does not name the declared coordinate:\n%s", report)
	}
}

// TestMissingSecretReport_LockedStoreIsNotAnAbsentCredential is the regression test for #61: a LOCKED
// keyring must NOT read as "store it" — the credential may be present and unreadable.
func TestMissingSecretReport_LockedStoreIsNotAnAbsentCredential(t *testing.T) {
	report, err := missingSecretReport("mybox", []spec.EnvDependency{dep("QDRANT__SERVICE__API_KEY", "charly/api-key/qdrant")},
		[]secretResolution{{Name: "QDRANT__SERVICE__API_KEY", Source: "locked", Resolved: false, Required: true}})
	if err == nil {
		t.Fatal("err = nil, want an error")
	}
	if strings.Contains(report, "Store them in the credential backend") {
		t.Errorf("a locked store still advises a store operation:\n%s", report)
	}
	if !strings.Contains(report, "could not READ the credential store") {
		t.Errorf("report does not name the unreadable store:\n%s", report)
	}
	if !strings.Contains(report, "source: locked") {
		t.Errorf("report does not name the resolver's source:\n%s", report)
	}
	if !strings.Contains(report, "charly/api-key/qdrant") {
		t.Errorf("report does not name the queried coordinate:\n%s", report)
	}
	if !strings.Contains(report, "charly secrets get charly/api-key qdrant") {
		t.Errorf("report does not give the operator a way to confirm the value:\n%s", report)
	}
	if !strings.Contains(err.Error(), "could not read the credential store") {
		t.Errorf("err = %v, want the unreadable-store class", err)
	}
}

// TestMissingSecretReport_UnavailableStore: no Secret Service at all is the same class — and its
// remedy is a provider or the config backend, never "set the secret again".
func TestMissingSecretReport_UnavailableStore(t *testing.T) {
	report, err := missingSecretReport("mybox", []spec.EnvDependency{dep("TOK", "")},
		[]secretResolution{{Name: "TOK", Source: "unavailable", Resolved: false, Required: true}})
	if err == nil {
		t.Fatal("err = nil, want an error")
	}
	if strings.Contains(report, "Store them in the credential backend") {
		t.Errorf("an unavailable store still advises a store operation:\n%s", report)
	}
	if !strings.Contains(report, "secret_backend config") {
		t.Errorf("report does not offer the headless remedy:\n%s", report)
	}
}

// TestMissingSecretReport_BothClassesReported: one locked credential and one genuinely absent one must
// NOT collapse into one message — the unreadable class is the error, and both sections print.
func TestMissingSecretReport_BothClassesReported(t *testing.T) {
	report, err := missingSecretReport("mybox",
		[]spec.EnvDependency{dep("LOCKED_ONE", ""), dep("ABSENT_ONE", "")},
		[]secretResolution{
			{Name: "LOCKED_ONE", Source: "locked", Resolved: false, Required: true},
			{Name: "ABSENT_ONE", Source: "default", Resolved: false, Required: true},
		})
	if err == nil {
		t.Fatal("err = nil, want an error")
	}
	if !strings.Contains(err.Error(), "could not read the credential store") {
		t.Errorf("err = %v, want the unreadable-store class to win", err)
	}
	for _, want := range []string{"could not READ the credential store", "Store them in the credential backend", "charly/secret/LOCKED_ONE", "charly/secret/ABSENT_ONE"} {
		if !strings.Contains(report, want) {
			t.Errorf("report missing %q:\n%s", want, report)
		}
	}
}
