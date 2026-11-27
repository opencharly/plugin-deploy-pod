package deploypod

// start_ready.go — the post-start CONTAINER-ready wait.
//
// `charly start` on a pod unit runs `systemctl --user start <svc>`, which returns as soon as the
// job is queued. The engine then CREATES the container and only afterwards starts it, and a
// container that is not yet `running` has an EMPTY mount namespace: `podman exec` into a
// created-but-not-started container succeeds and sees none of its mounts. A caller that acts on a
// bind-mounted path immediately after start therefore races it —
//
//	charly start <pod>            → rc=0
//	charly cmd <pod> "test -e /data" → rc=1 ("No such file or directory"), PRESENT ~1.6s later
//
// (measured 2026-11-27 on a keep-id + CDI pod; the filing measured an ~11s window, so it is a real
// and intermittent race, not a fixed delay — opencharly/charly#1301).
//
// The wait polls the ENGINE's own state until it reports the container running, bounded by the
// shared poll primitive. It is deliberately separate from the declared readiness wait
// (readiness.go): that one waits for the APPLICATION to accept work and only runs when the deploy
// authors a probe, while this one waits for the CONTAINER to exist and always runs.

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/opencharly/spec/poll"
	"github.com/opencharly/spec/shellquote"
	"github.com/opencharly/spec/spec"
)

const (
	// containerReadyInterval is the tick cadence; the bound is containerReadyDeadline. One second
	// keeps the window a caller sees at most that large while costing one inspect per tick.
	containerReadyInterval = 1 * time.Second
	// containerReadyPerAttempt bounds ONE inspect, so a hung engine cannot eat the whole deadline.
	containerReadyPerAttempt = 10 * time.Second
	// containerReadyDeadline bounds the whole wait. A container start that has not reached
	// `running` in a minute is not slow, it is broken, and the failure should say so.
	containerReadyDeadline = 60 * time.Second
)

// containerReadyPollConfig is the ONE production poll policy for the container-ready wait.
func containerReadyPollConfig(name string) poll.PollConfig {
	return poll.PollConfig{
		Name:        "container ready " + name,
		Interval:    containerReadyInterval,
		PerAttempt:  containerReadyPerAttempt,
		AbsoluteCap: containerReadyDeadline,
	}
}

// waitContainerRunning blocks until the engine reports the started container `running`, bounded by
// containerReadyPollConfig. A failed inspect is transient (the container may not be created yet, or
// the engine may still be replacing it after a restart); the deadline is what fails a container
// that never comes up.
func waitContainerRunning(ctx context.Context, ex venueExecutor, p lifecycleParams, plan spec.PodLifecyclePlan) error {
	if plan.ContainerName == "" {
		return fmt.Errorf("container-ready wait %q: the start plan carries no container name", p.Name)
	}
	engine := startEngineBin(p, plan)
	if engine == "" {
		return fmt.Errorf("container-ready wait %q: the start plan carries no container engine binary", p.Name)
	}
	script := engine + " inspect --format " + shellquote.ShellQuote("{{.State.Status}}") + " " + shellquote.ShellQuote(plan.ContainerName)
	return pollContainerRunning(ctx, containerReadyPollConfig(p.Name), ex, script)
}

// pollContainerRunning runs the engine's state inspect until it reports the container `running`,
// bounded by cfg. A failed inspect is transient (the container may not be created yet, or the
// engine may still be replacing it after a restart); the deadline is what fails a container that
// never comes up. Tests pass their own short cfg, exactly as pollReadiness does.
func pollContainerRunning(ctx context.Context, cfg poll.PollConfig, ex venueExecutor, script string) error {
	return poll.PollUntil(ctx, cfg, func(actx context.Context) (bool, float64, error) {
		out, err := ex.VenueCapture(actx, script)
		if err != nil {
			return false, 0, nil
		}
		return strings.TrimSpace(out) == "running", 0, nil
	})
}
