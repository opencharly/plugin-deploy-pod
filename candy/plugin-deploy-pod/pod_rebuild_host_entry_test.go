package deploypod

// pod_rebuild_host_entry_test.go — the decision that makes `charly update` work on a deploy that
// lives ONLY in the host config (opencharly/charly#1249).
//
// The rebuild sequence's `deploy add` leg is the PROJECT route: with no `<ref>` it resolves the
// deploy from the project's charly.yml. A host-config deploy has no project entry, so that leg
// failed with `no <ref> and charly.yml has no entry for "<name>"` and `update` was unusable on it.
// `podRebuildHasHostEntry` is the whole decision: entry present -> skip the leg and let `config`
// re-render from the host entry; entry absent -> keep the leg (the fresh-deploy case).
//
// The load-error arm is asserted too: an UNREADABLE config must NOT read as "no entry", because
// that would silently skip a fresh deploy's registration.

import (
	"context"
	"errors"
	"testing"

	"github.com/opencharly/sdk"
	"github.com/opencharly/sdk/deploykit"
	"github.com/opencharly/spec/spec"
)

func TestPodRebuildHasHostEntry(t *testing.T) {
	ctx := context.Background()

	withLoadDeploy := func(t *testing.T, fn func(context.Context, *sdk.Executor, string) (*deploykit.DeployConfig, error)) {
		t.Helper()
		orig := loadDeploy
		t.Cleanup(func() { loadDeploy = orig })
		loadDeploy = fn
	}

	t.Run("a host-config deploy skips the project route", func(t *testing.T) {
		dc := &deploykit.DeployConfig{Deploy: map[string]spec.DeployNode{
			"strata": {Image: "strata"},
		}}
		withLoadDeploy(t, func(context.Context, *sdk.Executor, string) (*deploykit.DeployConfig, error) {
			return dc, nil
		})
		if !podRebuildHasHostEntry(ctx, nil, "strata") {
			t.Fatal("a deploy recorded in the host config must be reported as present — otherwise `deploy add` runs and fails with `charly.yml has no entry` (opencharly/charly#1249)")
		}
	})

	t.Run("a fresh project-declared deploy keeps the project route", func(t *testing.T) {
		withLoadDeploy(t, func(context.Context, *sdk.Executor, string) (*deploykit.DeployConfig, error) {
			return &deploykit.DeployConfig{}, nil
		})
		if podRebuildHasHostEntry(ctx, nil, "check-sidecar-pod") {
			t.Fatal("a deploy with no host entry must NOT skip `deploy add` — that leg is how a fresh deploy is registered")
		}
	})

	t.Run("an unreadable config is not a no-entry answer", func(t *testing.T) {
		withLoadDeploy(t, func(context.Context, *sdk.Executor, string) (*deploykit.DeployConfig, error) {
			return nil, errors.New("no host reverse channel")
		})
		if podRebuildHasHostEntry(ctx, nil, "strata") {
			t.Fatal("a config read error must not be reported as an existing entry")
		}
	})

	t.Run("a nil config is not an existing entry", func(t *testing.T) {
		withLoadDeploy(t, func(context.Context, *sdk.Executor, string) (*deploykit.DeployConfig, error) {
			return nil, nil
		})
		if podRebuildHasHostEntry(ctx, nil, "strata") {
			t.Fatal("a nil config must not be reported as an existing entry")
		}
	})
}
