package deploypod

// readiness.go — the DECLARED post-start readiness wait for a pod deploy.
//
// `spec.Deploy.Probes.Readiness` (spec/schema/deploy.cue) is the authored readiness
// Op for a deploy. Until now no consumer read it, so `charly start` and `charly
// update` returned as soon as supervisord's children were RUNNING — which is also
// true of a start wrapper that is still waiting on its database. A bed that then
// probed the application raced it: check-cachyos-immich-pod's check-live-rebuild ran
// ~3s after the rebuild's restart and `pg_isready` failed, while the first live leg
// (with a longer head start) passed (opencharly/charly#1234).
//
// This wait runs the AUTHORED probe inside the container, bounded by the shared
// poll primitive, so the initial start and the post-update restart both return only
// once the application accepts work. It is deliberately separate from the
// acceptance checks: an acceptance failure is evidence and is never retried here.

import (
	"context"
	"fmt"
	"time"

	"github.com/opencharly/spec/poll"
	"github.com/opencharly/spec/shellquote"
	"github.com/opencharly/spec/spec"
)

const (
	// readinessInterval is the tick cadence of the declared-readiness poll. It is
	// pacing only; the bound is readinessDeadline.
	readinessInterval = 2 * time.Second
	// readinessPerAttempt bounds ONE probe execution, so a hung `sh -c` cannot eat
	// the whole deadline.
	readinessPerAttempt = 30 * time.Second
	// readinessDeadline is the total wall-clock bound. A first-run database
	// initialization on a loaded host is the slowest observed case; five minutes is
	// a generous ceiling that still fails a deploy whose readiness probe can never
	// pass.
	readinessDeadline = 5 * time.Minute
)

// declaredReadinessOp returns the authored probes.readiness Op, or nil when the
// deploy declares none.
func declaredReadinessOp(node *spec.Deploy) *spec.Op {
	if node == nil || node.Probes == nil {
		return nil
	}
	return node.Probes.Readiness
}

// readinessCommand extracts the shell command a readiness Op declares. A readiness
// probe must be a `command:` Op: the pod venue runs it with `sh -c` inside the
// container, so any other verb (which needs the check engine) is refused loudly
// rather than silently ignored.
func readinessCommand(op *spec.Op) (string, error) {
	if op == nil || op.Command == "" {
		if op == nil {
			return "", nil
		}
		return "", fmt.Errorf("probes.readiness must be a `command:` Op: the pod venue runs it with sh -c inside the container; use command: for a readiness probe")
	}
	return op.Command, nil
}

// readinessPollConfig is the ONE production poll policy for a declared readiness
// wait. Tests build their own config with short bounds.
func readinessPollConfig(name string) poll.PollConfig {
	return poll.PollConfig{
		Name:        "readiness " + name,
		Interval:    readinessInterval,
		PerAttempt:  readinessPerAttempt,
		AbsoluteCap: readinessDeadline,
	}
}

// pollReadiness runs the probe command inside the container until it exits 0,
// bounded by cfg. A non-zero exit is transient (the application is still starting);
// the per-attempt bound caps one hung probe.
func pollReadiness(ctx context.Context, cfg poll.PollConfig, ex venueExecutor, script string) error {
	return poll.PollUntil(ctx, cfg, func(actx context.Context) (bool, float64, error) {
		if _, err := ex.VenueCapture(actx, script); err != nil {
			return false, 0, nil
		}
		return true, 0, nil
	})
}

// startEngineBin resolves the container engine binary a post-start wait execs: the host-supplied
// lifecycle param first (it is what the host resolved for this operation), then the plan's own.
// Empty means neither carried one, which every caller reports rather than papering over.
func startEngineBin(p lifecycleParams, plan spec.PodLifecyclePlan) string {
	if p.EngineBin != "" {
		return p.EngineBin
	}
	return plan.EngineBin
}

// waitDeclaredReadiness runs the deploy's authored readiness probe after the
// container starts. It is a no-op for a deploy that declares none.
func waitDeclaredReadiness(ctx context.Context, ex venueExecutor, p lifecycleParams, plan spec.PodLifecyclePlan) error {
	cmd, err := readinessCommand(declaredReadinessOp(p.Node))
	if err != nil {
		return err
	}
	if cmd == "" {
		return nil
	}
	engine := startEngineBin(p, plan)
	if engine == "" {
		return fmt.Errorf("readiness probe %q: the start plan carries no container engine binary", p.Name)
	}
	if plan.ContainerName == "" {
		return fmt.Errorf("readiness probe %q: the start plan carries no container name", p.Name)
	}
	script := engine + " exec " + shellquote.ShellQuote(plan.ContainerName) + " sh -c " + shellquote.ShellQuote(cmd)
	return pollReadiness(ctx, readinessPollConfig(p.Name), ex, script)
}
