package deploypod

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// fakeVenue is the venue seam, answering BOTH primitives the way the real pair does: the capturing
// one with the venue's own stderr, the silent one with nothing but the exit code. That is what makes
// the plugin's CHOICE between them measurable — the defect in opencharly/plugin-deploy-pod#19 was
// never that the cause was unavailable, it was that the code asked for the primitive that drops it.
type fakeVenue struct {
	exit                   int // 0 = the command succeeded
	stderr                 string
	script                 string
	captureCalls, runCalls int
}

// The two methods reproduce spec/exec/executor.go's contract VERBATIM (read on the pinned spec
// v0.2026276.0): VenueCapture returns stdout, and on a non-zero exit the TRIMMED stderr when there
// is one, else `command exited %d`; VenueRunSilent discards both streams and returns only
// `command exited %d`. Faking the pair's real asymmetry—not a convenient one—is what makes the
// mutation control meaningful.
func (f *fakeVenue) VenueCapture(_ context.Context, script string) (string, error) {
	f.captureCalls++
	f.script = script
	if f.exit != 0 {
		if s := strings.TrimSpace(f.stderr); s != "" {
			return "", errors.New(s)
		}
		return "", fmt.Errorf("command exited %d", f.exit)
	}
	return "captured stdout", nil
}

func (f *fakeVenue) VenueRunSilent(_ context.Context, script string) error {
	f.runCalls++
	f.script = script
	if f.exit != 0 {
		return fmt.Errorf("command exited %d", f.exit)
	}
	return nil
}

// measuredTagStderr is the venue's OWN answer for the exact command the deploy-name alias tag runs,
// captured verbatim on this host (2026-10-07) — the cause the silent primitive used to discard:
//
//	$ podman tag ghcr.io/opencharly/no-such-image:latest localhost/spike-alias:2026.280.0000
//	exit=125
//	stderr: [Error: ghcr.io/opencharly/no-such-image:latest: image not known]
const measuredTagStderr = "Error: ghcr.io/opencharly/no-such-image:latest: image not known"

// TestVenueRunSurfacesTheVenueCause is the regression guard for plugin-deploy-pod#19: a failing venue
// step must reach the operator as something they can act on — the venue's own stderr, the operation,
// and the artifact it was operating on — and the helper must reach it through the CAPTURING
// primitive, never the silent one.
//
// Mutation control: swapping the call inside venueRun for VenueRunSilent turns this test red twice
// over — the message loses `image not known` (becoming the bare `command exited 125` the issue
// reports) and runCalls becomes 1. That is why VenueRunSilent is part of the seam interface.
func TestVenueRunSurfacesTheVenueCause(t *testing.T) {
	v := &fakeVenue{exit: 125, stderr: measuredTagStderr}
	err := venueRun(context.Background(), v,
		"podman tag 'a:1' 'b:2'", "deploy-name alias tag", "my-deploy")
	if err == nil {
		t.Fatal("a failing venue step reported success")
	}
	msg := err.Error()
	if !strings.Contains(msg, measuredTagStderr) {
		t.Errorf("the venue's own cause is missing from the error — this is the whole defect:\n got: %q\nwant it to contain: %q", msg, measuredTagStderr)
	}
	for _, want := range []string{"plugin-deploy-pod", "deploy-name alias tag", "my-deploy"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q does not name %q — an unattributable failure is what #19 is about", msg, want)
		}
	}
	if v.runCalls != 0 {
		t.Errorf("venueRun called the SILENT primitive %d time(s); it must use the capturing one", v.runCalls)
	}
	if v.captureCalls != 1 {
		t.Errorf("venueRun called VenueCapture %d time(s), want exactly 1", v.captureCalls)
	}
}

// TestVenueRunKeepsASilentFailureAttributable is the other half: when the venue prints NOTHING, the
// failure must still be attributable from the exit code plus this plugin's own label — the property
// the old wrapper half-had, now pinned so a future simplification cannot drop it.
func TestVenueRunKeepsASilentFailureAttributable(t *testing.T) {
	v := &fakeVenue{exit: 125} // non-zero exit, NOTHING on stderr
	err := venueRun(context.Background(), v, "podman start x", "start", "x")
	if err == nil {
		t.Fatal("a failing venue step reported success")
	}
	msg := err.Error()
	for _, want := range []string{"command exited 125", "plugin-deploy-pod start (x)"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q does not name %q", msg, want)
		}
	}
}

// TestVenueRunPassesTheScriptThroughUnchanged guards the one thing a wrapper like this can silently
// break: the exact command it was asked to run. A shell-quoted tag script must reach the venue
// byte-for-byte.
func TestVenueRunPassesTheScriptThroughUnchanged(t *testing.T) {
	const script = "podman tag 'ghcr.io/opencharly/x:2026.280.0000' 'ghcr.io/opencharly/y:2026.280.0000'"
	v := &fakeVenue{}
	if err := venueRun(context.Background(), v, script, "deploy-name alias tag", "y"); err != nil {
		t.Fatalf("venueRun: %v", err)
	}
	if v.script != script {
		t.Errorf("the venue received %q, want the script verbatim %q", v.script, script)
	}
}
