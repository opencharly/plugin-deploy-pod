package deploypod

// start_ready_test.go — the post-start container-ready wait (opencharly/charly#1301).
//
// The witness is the POLL, not a single read: a venue that reports `created` twice before
// `running` must be inspected at least three times, because the whole defect is that the start
// command returns while the container is still `created` (an empty mount namespace).

import (
	"context"
	"strings"
	"testing"

	"github.com/opencharly/spec/spec"
)

// scriptedVenue returns a scripted sequence of inspect outputs; once the sequence is exhausted it
// repeats its last entry, so a "never runs" arm is expressible.
type scriptedVenue struct {
	states []string
	calls  int
	script string
}

func (v *scriptedVenue) VenueCapture(_ context.Context, script string) (string, error) {
	v.script = script
	i := v.calls
	v.calls++
	if i >= len(v.states) {
		i = len(v.states) - 1
	}
	return v.states[i], nil
}

func (v *scriptedVenue) VenueRunSilent(context.Context, string) error { return nil }

func TestPollContainerRunning_WaitsThroughCreatedUntilRunning(t *testing.T) {
	ex := &scriptedVenue{states: []string{"created", "created", "running"}}
	script := `podman inspect --format '{{.State.Status}}' charly-demo`
	if err := pollContainerRunning(context.Background(), testPollConfig(), ex, script); err != nil {
		t.Fatalf("pollContainerRunning: %v", err)
	}
	if ex.calls != 3 {
		t.Fatalf("inspect ran %d times, want 3 (two `created` then `running`) — a single read would return while the container has no mounts", ex.calls)
	}
}

func TestPollContainerRunning_FailsAContainerThatNeverRuns(t *testing.T) {
	ex := &scriptedVenue{states: []string{"created"}}
	err := pollContainerRunning(context.Background(), testPollConfig(), ex, `podman inspect --format '{{.State.Status}}' charly-demo`)
	if err == nil {
		t.Fatal("a container that never reaches `running` must fail the wait, not hang or pass")
	}
	if ex.calls < 2 {
		t.Fatalf("inspect ran %d time(s), want repeated until the deadline", ex.calls)
	}
}

func TestWaitContainerRunning_InspectsTheContainersOwnState(t *testing.T) {
	ex := &scriptedVenue{states: []string{"running"}}
	plan := spec.PodLifecyclePlan{ContainerName: "charly-demo", EngineBin: "podman"}
	if err := waitContainerRunning(context.Background(), ex, lifecycleParams{Name: "demo"}, plan); err != nil {
		t.Fatalf("waitContainerRunning: %v", err)
	}
	if !strings.Contains(ex.script, "inspect") || !strings.Contains(ex.script, "charly-demo") {
		t.Fatalf("the wait inspected %q, want the container's own State.Status", ex.script)
	}
}

func TestWaitContainerRunning_RefusesAPlanWithNoContainerOrEngine(t *testing.T) {
	for _, plan := range []spec.PodLifecyclePlan{
		{EngineBin: "podman"},
		{ContainerName: "charly-demo"},
	} {
		err := waitContainerRunning(context.Background(), &scriptedVenue{states: []string{"running"}}, lifecycleParams{Name: "demo"}, plan)
		if err == nil {
			t.Fatalf("plan %+v must be refused rather than silently skipped: a start that cannot be waited on is the bug", plan)
		}
	}
}
