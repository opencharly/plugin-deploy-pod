package deploypod

import (
	"context"
	"fmt"
)

// venue_exec.go — the ONE way this plugin runs a shell command on the venue.
//
// Why it exists: `VenueRunSilent` DISCARDS both streams of the command it runs, so a failure at the
// boundary reached the operator as a bare `command exited 125` — with nothing naming the image, the
// reason, or the venue. MEASURED on this host, running the exact command the deploy-name alias tag
// runs:
//
//	$ podman tag ghcr.io/opencharly/no-such-image:latest localhost/spike-alias:2026.280.0000
//	exit=125
//	stderr: [Error: ghcr.io/opencharly/no-such-image:latest: image not known]
//
// The cause existed and was one clear line long; the helper threw it away before anyone could read
// it (opencharly/plugin-deploy-pod#19). That is the same class of loss that left
// opencharly/opencharly#322's orphaned bed pod unattributable: charly has a rich catalogue of failure
// causes and a habit of discarding them exactly where they cross a process boundary.
//
// `VenueCapture` — the sibling primitive over the SAME reverse channel — already returns the venue's
// trimmed stderr on a non-zero exit, so the cure is not a new mechanism: it is to stop choosing the
// primitive that loses the cause. Every venue step in this plugin now goes through `venueRun`, so
// there is no second path that can drop one.

// venueExecutor is the seam `venueRun` takes: the two venue-exec primitives of the served host
// executor (`sdk.Executor`, an alias of `spec/exec.Executor`, satisfies it). Taking the interface
// rather than the concrete client is what makes the one property that matters testable without a
// live venue — that this helper asks for the venue's OUTPUT.
//
// `VenueRunSilent` is deliberately PART OF THE SEAM although `venueRun` must never call it: the
// mutation control in venue_exec_test.go pins the difference between the two primitives, so a future
// reader cannot silently swap the capturing call for the silent one without turning that test red.
type venueExecutor interface {
	VenueCapture(ctx context.Context, script string) (string, error)
	VenueRunSilent(ctx context.Context, script string) error
}

// venueRun runs one shell command on the venue and, on failure, returns an error carrying the
// venue's OWN cause (its stderr, or its exit code when it printed nothing) behind this plugin's
// operation label and target name — the shape every caller already used for the label, now with the
// cause it was missing. stdout is discarded: every command it drives (a build, a tag, a chown, a
// unit start/stop) is fire-and-forget, and none of them is a data-producing read.
func venueRun(ctx context.Context, ex venueExecutor, script, label, target string) error {
	if _, err := ex.VenueCapture(ctx, script); err != nil {
		return fmt.Errorf("plugin-deploy-pod %s (%s): %w", label, target, err)
	}
	return nil
}
