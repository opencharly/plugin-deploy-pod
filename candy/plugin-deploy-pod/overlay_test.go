package deploypod

// overlay_test.go — the unit harness issue #68 names as option 1: the PLAN-FACING half of the
// overlay path, driven end to end with no substrate.
//
// WHY THIS HALF AND NOT `buildOverlay`. `buildOverlay` needs a live `*sdk.Executor`: it builds a
// render generator, walks each plan's steps through `deploykit.OCITarget.Emit` (whose `EmitStepOp`
// calls `exec.HostBuild("step-emit", …)` per step), renders service fragments through the
// provider registry, and writes the Containerfile. None of that is a container ENGINE —
// the engine is only reached after `opts.DryRun` — but all of it is the host executor, which a
// unit test does not have. So this harness drives the functions that consume the same inputs
// (`plans`, and through `AddCandies` the overlay candy set) and ARE the plan-facing behaviour:
// `collectOverlayCandies` + `filterPlansByCandies`. What that leaves unproven is named rather than
// implied: the render half (`oci.Emit` → the host step-emit seam) is proven by a bed, which is
// #68's option 2 and is the R10 the overlay path still owes.
//
// THE PROPERTY, NOT THE MECHANICS. A candy's `use: secret:` is compiled HOST-SIDE into a
// `spec.FileStep` placed BEFORE that candy's own steps (D-SEC-1: a secret reaches a task as a FILE's
// content on the venue, never as an environment value). The overlay path must therefore (1) keep
// that FileStep, (2) add no value to any `OpStep.Op.Env`, and (3) leave the compiler's step order
// alone — otherwise a task that reads the file finds it missing.

import (
	"reflect"
	"strings"
	"testing"

	"github.com/opencharly/sdk/deploykit"
	"github.com/opencharly/spec/spec"
)

// secretContents is the value the fixture's `use: secret:` resolves to. It is a canary: no byte of
// it may appear in an Op step's environment after the overlay plan set is computed. It is NOT a
// real credential and never leaves this file.
const secretContents = "s3cr3t-value-that-must-never-be-recorded"

// overlayFixture is the plans the host compiler produces for a pod that adds one overlay candy
// (`svc-secret`) which authors `use: secret:`:
//
//   - the deploy's own plan (`pod-app`) NAMES the overlay candy in its add_candy list;
//   - the overlay candy's plan (`svc-secret`) is the one the overlay build RENDERS, and the compiler
//     has already placed that secret's FileStep FIRST in its step sequence, with the candy's own Op
//     steps after it;
//   - the base image's plan (`base-layer`) is already baked and must not enter the overlay set.
func overlayFixture() []*spec.InstallPlan {
	compiledSecretFile := &spec.FileStep{
		Source:    ".build/_inline/svc-secret.token",
		Dest:      "/run/secrets/token",
		Mode:      0o600,
		Owner:     "root",
		CandyName: "svc-secret",
	}
	return []*spec.InstallPlan{
		{
			DeployID:   "fx-deploy",
			Box:        "pod-app",
			Version:    "2026.320.0800",
			Distro:     "fedora:43",
			Candy:      "pod-app",
			AddCandies: []string{"svc-secret"},
			Steps: []spec.InstallStep{
				&spec.OpStep{Op: &spec.Op{Env: map[string]string{"HOME": "/home/app"}}, CandyName: "pod-app"},
			},
		},
		{
			DeployID: "fx-deploy",
			Box:      "pod-app",
			Version:  "2026.320.0800",
			Distro:   "fedora:43",
			Candy:    "svc-secret",
			Steps: []spec.InstallStep{
				compiledSecretFile,
				&spec.OpStep{Op: &spec.Op{Mkdir: "/run/secrets"}, CandyName: "svc-secret"},
				&spec.OpStep{Op: &spec.Op{Env: map[string]string{"HOME": "/home/app"}}, CandyName: "svc-secret"},
			},
		},
		{
			DeployID: "fx-deploy",
			Box:      "pod-app",
			Version:  "2026.320.0800",
			Distro:   "fedora:43",
			Candy:    "base-layer",
			Steps: []spec.InstallStep{
				&spec.OpStep{Op: &spec.Op{Env: map[string]string{"HOME": "/root"}}, CandyName: "base-layer"},
			},
		},
	}
}

// overlayPlanSet is the overlay path's plan pipeline: the same two calls `buildOverlay` makes
// (`collectOverlayCandies` then `filterPlansByCandies`).
func overlayPlanSet(plans []*spec.InstallPlan) []*spec.InstallPlan {
	return filterPlansByCandies(plans, collectOverlayCandies(plans))
}

// assertOverlaySetHasCompiledFileStep is property (1): the compiled FileStep survives, unchanged.
func assertOverlaySetHasCompiledFileStep(t *testing.T, plans []*spec.InstallPlan) {
	t.Helper()
	out := overlayPlanSet(plans)
	if len(out) != 1 || out[0].Candy != "svc-secret" {
		t.Fatalf("overlay plan set = %d plan(s) %v, want exactly the overlay candy's plan [svc-secret] — the set is every plan FOR a candy an add_candy list names", len(out), planCandies(out))
	}
	var got *spec.FileStep
	for _, s := range out[0].Steps {
		if f, ok := s.(*spec.FileStep); ok {
			got = f
		}
	}
	if got == nil {
		t.Fatalf("the compiled FileStep did not survive the overlay plan set: steps = %v\n"+
			"a `use: secret:` candy's secret reaches its task as this file, so dropping the step drops the secret", stepKinds(out[0]))
	}
	want := &spec.FileStep{
		Source:    ".build/_inline/svc-secret.token",
		Dest:      "/run/secrets/token",
		Mode:      0o600,
		Owner:     "root",
		CandyName: "svc-secret",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("the FileStep changed in the overlay plan set:\n got %+v\nwant %+v", got, want)
	}
}

// assertNoSecretValueInAnOpEnv is property (2), the D-SEC-1 absence: no Op step's environment
// carries the secret's content, and the pipeline adds no key to any environment it was given.
func assertNoSecretValueInAnOpEnv(t *testing.T, plans []*spec.InstallPlan) {
	t.Helper()
	out := overlayPlanSet(plans)
	for _, p := range out {
		for i, s := range p.Steps {
			op, ok := s.(*spec.OpStep)
			if !ok || op.Op == nil {
				continue
			}
			for k, v := range op.Op.Env {
				if strings.Contains(k, secretContents) || strings.Contains(v, secretContents) {
					t.Fatalf("plan %q step %d (%s) carries the secret's CONTENT in its environment: %s=%q\n"+
						"D-SEC-1: a secret never enters IR/state/output — it reaches the task as the Content of the FileStep the compiler placed", p.Candy, i, s.Kind(), k, v)
				}
			}
			want := map[string]string{}
			if op.CandyName == "svc-secret" && op.Op.Mkdir == "" {
				want = map[string]string{"HOME": "/home/app"}
			}
			if !reflect.DeepEqual(mapOrEmpty(op.Op.Env), want) {
				t.Fatalf("plan %q step %d environment = %v, want exactly %v — the overlay plan set SELECTS plans and must add nothing to a step's environment", p.Candy, i, op.Op.Env, want)
			}
		}
	}
}

// assertCompilerStepOrderIsPreserved is property (3): the FileStep still precedes the candy's own
// Op steps, and the whole sequence is the compiler's.
func assertCompilerStepOrderIsPreserved(t *testing.T, plans []*spec.InstallPlan) {
	t.Helper()
	out := overlayPlanSet(plans)
	if len(out) != 1 {
		t.Fatalf("overlay plan set = %d plan(s), want 1", len(out))
	}
	fileAt, firstOpAt := -1, -1
	for i, s := range out[0].Steps {
		switch s.Kind() {
		case spec.StepKindFile:
			if fileAt < 0 {
				fileAt = i
			}
		case spec.StepKindOp:
			if firstOpAt < 0 {
				firstOpAt = i
			}
		}
	}
	if fileAt < 0 || firstOpAt < 0 {
		t.Fatalf("the plan no longer carries both a File step and an Op step: %v", stepKinds(out[0]))
	}
	if fileAt > firstOpAt {
		t.Fatalf("the FileStep is at %d and the candy's first Op step at %d — the file must come FIRST, or a task that reads it finds nothing (steps: %v)", fileAt, firstOpAt, stepKinds(out[0]))
	}
	if got, want := stepKinds(out[0]), stepKinds(plans[1]); !reflect.DeepEqual(got, want) {
		t.Fatalf("the overlay plan set reordered the compiler's steps:\n got %v\nwant %v", got, want)
	}
}

func TestOverlayPlanSetKeepsTheCompiledFileStep(t *testing.T) {
	assertOverlaySetHasCompiledFileStep(t, overlayFixture())
}

func TestOverlayPlanSetAddsNoSecretValueToAnOpEnv(t *testing.T) {
	assertNoSecretValueInAnOpEnv(t, overlayFixture())
}

func TestOverlayPlanSetPreservesTheCompilerStepOrder(t *testing.T) {
	assertCompilerStepOrderIsPreserved(t, overlayFixture())
}

func planCandies(plans []*spec.InstallPlan) []string {
	out := make([]string, 0, len(plans))
	for _, p := range plans {
		out = append(out, p.Candy)
	}
	return out
}

func stepKinds(p *spec.InstallPlan) []string {
	out := make([]string, 0, len(p.Steps))
	for _, s := range p.Steps {
		out = append(out, string(s.Kind()))
	}
	return out
}

func mapOrEmpty(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return m
}

// TestDeliverSecretEnvIsANoOpWhenNoCandyAsks drives `buildOverlay`'s OWN secret prefix — the
// function the extraction exists for — rather than only the plan set it feeds. With no candy
// asking for a secret (the state of the tree: no candy authors `use: secret:`, measured 0) the step
// must be a no-op: the credential store is never consulted, no environment key is added, and the
// compiler's FileStep and its order survive untouched. This is the assertion that binds to the
// code path a re-added injection would ship through.
func TestDeliverSecretEnvIsANoOpWhenNoCandyAsks(t *testing.T) {
	plans := overlayFixture()
	consulted := false
	// A resolver that WOULD hand over the secret: if the function asks for it with no candy
	// requesting one, that is a defect the assertion below reports rather than a value it injects.
	cred := deploykit.CredentialAccess{Resolve: func(envVar, service, key, defaultVal string) (string, string) {
		consulted = true
		return secretContents, "test"
	}}
	// The candy EXISTS and a plan references it, so `SelectCandiesForPlans` really selects it and
	// `ResolveSecretForCandy` really walks it — the only thing absent is a `use: secret:` author,
	// which is the tree's state. An empty map would short-circuit the selection and prove nothing.
	candies := map[string]spec.CandyReader{
		"svc-secret": deploykit.NewSpecCandyModel(spec.CandyModel{Name: "svc-secret"}, spec.CandyView{}),
	}
	if got := deploykit.SelectCandiesForPlans(plans, candies); len(got) != 1 {
		t.Fatalf("fixture: the selected candy set = %d, want 1 — the test must exercise the selection, not skip it", len(got))
	}
	deliverSecretEnv(plans, candies, cred)
	if consulted {
		t.Fatalf("the credential store was consulted although no candy asks for a secret (D-SEC-1: ask nothing, hold nothing)")
	}
	assertNoSecretValueInAnOpEnv(t, plans)
	assertOverlaySetHasCompiledFileStep(t, plans)
	assertCompilerStepOrderIsPreserved(t, plans)
}
