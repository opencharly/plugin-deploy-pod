package deploypod

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/opencharly/spec/poll"
	"github.com/opencharly/spec/spec"
)

// flakyVenue is the readiness poll's seam: it fails the first `failFirst` probes and
// succeeds afterwards, so the transition and the deadline are testable without a
// live container. (The sibling venue_exec_test.go's fakeVenue is a fixed-exit fake;
// this one is the sequence fake the poll needs, under its own name.)
type flakyVenue struct {
	calls     int
	failFirst int
	script    string
}

func (f *flakyVenue) VenueCapture(_ context.Context, script string) (string, error) {
	f.calls++
	f.script = script
	if f.calls <= f.failFirst {
		return "", errors.New("not ready")
	}
	return "", nil
}

func (f *flakyVenue) VenueRunSilent(context.Context, string) error { return nil }

func testPollConfig() poll.PollConfig {
	return poll.PollConfig{
		Name:        "readiness test",
		Interval:    time.Millisecond,
		PerAttempt:  10 * time.Millisecond,
		AbsoluteCap: 50 * time.Millisecond,
	}
}

func TestReadinessCommand_CommandOp(t *testing.T) {
	got, err := readinessCommand(&spec.Op{Command: `pg_isready -h "$HOME/.postgresql"`})
	if err != nil {
		t.Fatalf("readinessCommand: %v", err)
	}
	if want := `pg_isready -h "$HOME/.postgresql"`; got != want {
		t.Fatalf("readinessCommand = %q, want %q", got, want)
	}
}

func TestReadinessCommand_RejectsNonCommandOp(t *testing.T) {
	_, err := readinessCommand(&spec.Op{ID: "ready"})
	if err == nil || !strings.Contains(err.Error(), "command:") {
		t.Fatalf("a readiness Op with no command must be refused with a command: hint, got %v", err)
	}
}

func TestWaitDeclaredReadiness_NoProbeIsNoOp(t *testing.T) {
	ex := &fakeVenue{}
	plan := spec.PodLifecyclePlan{ContainerName: "charly-demo", EngineBin: "podman"}
	if err := waitDeclaredReadiness(context.Background(), ex, lifecycleParams{Name: "demo"}, plan); err != nil {
		t.Fatalf("no readiness probe must be a no-op: %v", err)
	}
	if ex.captureCalls != 0 {
		t.Fatalf("no readiness probe ran %d probe(s), want 0", ex.captureCalls)
	}
}

func TestPollReadiness_PollsUntilTheProbeSucceeds(t *testing.T) {
	ex := &flakyVenue{failFirst: 2}
	script := `podman exec charly-demo sh -c 'pg_isready'`
	if err := pollReadiness(context.Background(), testPollConfig(), ex, script); err != nil {
		t.Fatalf("pollReadiness: %v", err)
	}
	if ex.calls != 3 {
		t.Fatalf("probe ran %d times, want 3 (two transient failures then ready)", ex.calls)
	}
	if ex.script != script {
		t.Fatalf("probe script = %q, want %q", ex.script, script)
	}
}

func TestPollReadiness_DeadlineFailsTheStart(t *testing.T) {
	ex := &flakyVenue{failFirst: 1 << 20}
	err := pollReadiness(context.Background(), testPollConfig(), ex, "podman exec charly-demo sh -c 'false'")
	if !errors.Is(err, poll.ErrPollCapExceeded) {
		t.Fatalf("a never-ready probe must fail with ErrPollCapExceeded, got %v", err)
	}
}

func TestWaitDeclaredReadiness_BuildsTheContainerProbe(t *testing.T) {
	ex := &fakeVenue{}
	node := &spec.Deploy{Probes: &spec.DeployProbes{Readiness: &spec.Op{Command: "curl -fsS http://127.0.0.1:2283/api/server/ping"}}}
	plan := spec.PodLifecyclePlan{ContainerName: "charly-demo", EngineBin: "podman"}
	if err := waitDeclaredReadiness(context.Background(), ex, lifecycleParams{Name: "demo", Node: node}, plan); err != nil {
		t.Fatalf("waitDeclaredReadiness: %v", err)
	}
	if ex.captureCalls != 1 {
		t.Fatalf("probe ran %d time(s), want 1", ex.captureCalls)
	}
	if !strings.Contains(ex.script, "exec") || !strings.Contains(ex.script, "charly-demo") || !strings.Contains(ex.script, "curl -fsS") {
		t.Fatalf("probe script %q does not run the authored command inside the container", ex.script)
	}
}
