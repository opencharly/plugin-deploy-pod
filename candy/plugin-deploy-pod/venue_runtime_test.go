package deploypod

// venue_runtime_test.go — the venue-handler gate (opencharly/plugin-containerd#16) and the
// failed-start cleanup.
//
// The gate arms describe the VENUE with PATH, because that is the resolution nerdctl applies
// to a bare `--runtime` word (exec.LookPath) — the same resolution the emitted unit's
// ExecStart will perform. A test that inherited the machine's PATH could not state which
// venue it is describing.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/opencharly/sdk"
	"github.com/opencharly/sdk/deploykit"
	"github.com/opencharly/spec/hostenv"
	"github.com/opencharly/spec/spec"
)

// setVenuePath makes PATH contain ONLY the named executables, so the test states the venue
// instead of inheriting the machine's own runtime installs.
func setVenuePath(t *testing.T, binaries ...string) {
	t.Helper()
	dir := t.TempDir()
	for _, name := range binaries {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatalf("write venue binary %s: %v", name, err)
		}
	}
	t.Setenv("PATH", dir)
}

// TestEnsureVenueRuntimeHandler_RefusesHandlerAbsentFromVenue is the decision arm: a venue that
// serves runc but NOT crun must be refused with all three facts — the handler charly selected,
// the handlers the venue reports, and the remedy.
func TestEnsureVenueRuntimeHandler_RefusesHandlerAbsentFromVenue(t *testing.T) {
	setVenuePath(t, "runc", "containerd-shim-runc-v2")

	err := ensureVenueRuntimeHandler("containerd")
	if err == nil {
		t.Fatal("a venue without crun must be refused, not launched with an unresolvable handler")
	}
	msg := err.Error()
	for _, want := range []string{`"crun"`, "runc", "remedy"} {
		if !strings.Contains(msg, want) {
			t.Errorf("refusal must name %q, got: %v", want, err)
		}
	}
	if strings.Contains(msg, `"krun"`) || strings.Contains(msg, `"nvidia"`) {
		t.Errorf("the refusal must not claim the venue reports handlers it does not have: %v", err)
	}

	// The observed set is exactly the engine's candidates the venue can launch — one source of
	// truth (the capability row), not a second hand-written list.
	c, ok := engineCapabilityForTest("containerd")
	if !ok {
		t.Fatal("containerd capability row missing")
	}
	if got := strings.Join(venueRuntimeHandlers(c), ","); got != "runc" {
		t.Errorf("venue handlers: got %q, want runc", got)
	}
}

// TestRuntimeHandlerBinary_RuntimeTypeResolvesToShim pins the runtime-TYPE arm: containerd
// resolves `io.containerd.runc.v2` through its shim binary, not through a same-named executable.
func TestRuntimeHandlerBinary_RuntimeTypeResolvesToShim(t *testing.T) {
	cases := map[string]string{
		"crun":                  "crun",
		"/usr/bin/crun":         "/usr/bin/crun",
		"io.containerd.runc.v2": "containerd-shim-runc-v2",
		"io.containerd.crun.v1": "containerd-shim-crun-v1",
		"wtf.sbk.runj.v1":       "containerd-shim-runj-v1",
	}
	for handler, want := range cases {
		if got := runtimeHandlerBinary(handler); got != want {
			t.Errorf("runtimeHandlerBinary(%q) = %q, want %q", handler, got, want)
		}
	}
}

// TestEnsureVenueRuntimeHandler_ProceedsWhenVenueHasSelectedHandler is the control: a venue that
// serves the selected handler proceeds with NO refusal and NO fallback — the same `crun` word.
func TestEnsureVenueRuntimeHandler_ProceedsWhenVenueHasSelectedHandler(t *testing.T) {
	setVenuePath(t, "crun", "runc")

	if err := ensureVenueRuntimeHandler("containerd"); err != nil {
		t.Fatalf("a venue with crun must not be refused: %v", err)
	}
}

// TestEnsureVenueRuntimeHandler_PodmanAndDockerUnchanged pins the no-new-failure-mode rule: an
// engine with an empty runtime_handlers list takes exactly the path it took before, on a venue
// that has NO container runtime binaries at all.
func TestEnsureVenueRuntimeHandler_PodmanAndDockerUnchanged(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // nothing installed

	for _, engine := range []spec.EngineName{"podman", "docker"} {
		if err := ensureVenueRuntimeHandler(engine); err != nil {
			t.Errorf("%s has no runtime_handlers list; the gate must not fire: %v", engine, err)
		}
	}
}

// TestRunPodConfigSetup_RefusesHandlerAbsentFromVenue drives the REAL config entry point, so the
// gate is proved where the defect fires: `charly config` on a containerd pod deploy whose venue
// has no crun must refuse BEFORE the config walk, so no unit is emitted and no restart loop can
// start.
//
// NEGATIVE CONTROL: on origin/main this test FAILS — the config walk proceeds (panics on the
// absent executor here) instead of refusing, and its error, if any, does not name crun.
func TestRunPodConfigSetup_RefusesHandlerAbsentFromVenue(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yml")
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmpDir, "xdg"))

	orig := hostenv.RuntimeConfigPath
	defer func() { hostenv.RuntimeConfigPath = orig }()
	hostenv.RuntimeConfigPath = func() (string, error) { return configPath, nil }

	t.Setenv("CHARLY_RUN_ENGINE", "containerd")
	t.Setenv("CHARLY_RUN_MODE", "systemd-unit")
	t.Setenv("CHARLY_ENGINE_ROOTFUL", "auto")
	setVenuePath(t, "runc") // the venue serves runc; crun is absent

	origLoad := loadDeploy
	defer func() { loadDeploy = origLoad }()
	loadDeploy = func(context.Context, *sdk.Executor, string) (*deploykit.DeployConfig, error) {
		return nil, nil // no per-host overlay: the selection resolves from the runtime level
	}

	var (
		err       error
		proceeded bool
	)
	func() {
		defer func() {
			if r := recover(); r != nil {
				proceeded = true // the config walk ran: nothing refused it
			}
		}()
		err = runPodConfigSetup(context.Background(), nil, &spec.PodConfigSetupRequest{Box: "demo"})
	}()
	if proceeded || err == nil {
		t.Fatal("charly config proceeded into the config walk on a venue without crun; it must refuse first (plugin-containerd#16)")
	}
	for _, want := range []string{`"crun"`, "runc", "remedy"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal must name %q, got: %v", want, err)
		}
	}

	// A refusal means NO unit was emitted and NO quadlet was written for the deploy.
	if _, statErr := os.Stat(filepath.Join(tmpDir, "xdg", "systemd", "user", "charly-demo.service")); !os.IsNotExist(statErr) {
		t.Errorf("a refused config must not emit a unit (stat err=%v)", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(tmpDir, "xdg", "containers", "systemd", "charly-demo.container")); !os.IsNotExist(statErr) {
		t.Errorf("a refused config must not write a quadlet (stat err=%v)", statErr)
	}
}

// recordingVenue is the venue seam for the cleanup arms: it records every script and fails the
// one the test names.
type recordingVenue struct {
	calls  []string
	failOn string
}

func (v *recordingVenue) VenueCapture(_ context.Context, script string) (string, error) {
	v.calls = append(v.calls, script)
	if v.failOn != "" && strings.Contains(script, v.failOn) {
		return "", errors.New("exit status 1")
	}
	return "", nil
}

func (v *recordingVenue) VenueRunSilent(context.Context, string) error { return nil }

// TestPodContainerStart_CleansUpFailedUnitStart proves the second half of #16: a failed unit
// start stops the unit (so Restart=always cannot re-create the container) and removes the
// partial container through the resolved containerd target, freeing the name for a retry.
func TestPodContainerStart_CleansUpFailedUnitStart(t *testing.T) {
	sel := selectionForTest(t, deploykit.Selection{}, "containerd")
	plan := spec.PodLifecyclePlan{
		Mode:          "systemd-unit",
		SvcName:       "charly-demo.service",
		ContainerName: "charly-demo",
		EngineBin:     "nerdctl",
	}
	venue := &recordingVenue{failOn: "systemctl --user start"}

	if err := podContainerStart(context.Background(), venue, plan, sel); err == nil {
		t.Fatal("a failing start must be reported")
	}
	want := []string{
		"systemctl --user start 'charly-demo.service'",
		"systemctl --user stop 'charly-demo.service'",
		"'nerdctl' '--address' '" + sel.Target.Address + "' '--namespace' '" + sel.Target.Namespace + "' 'rm' '-f' 'charly-demo'",
	}
	if len(venue.calls) != len(want) {
		t.Fatalf("cleanup calls: got %v, want %v", venue.calls, want)
	}
	for i := range want {
		if venue.calls[i] != want[i] {
			t.Errorf("cleanup call %d: got %q, want %q", i, venue.calls[i], want[i])
		}
	}
	if sel.Target.Address == "" || sel.Target.Namespace == "" {
		t.Fatalf("test target is not resolved: %+v", sel.Target)
	}
}

// TestPodContainerStart_CleansUpFailedDirectStart is the same proof for a direct launch: no unit
// to stop, so the container itself must be removed with the SAME argv shape.
func TestPodContainerStart_CleansUpFailedDirectStart(t *testing.T) {
	sel := selectionForTest(t, deploykit.Selection{}, "containerd")
	plan := spec.PodLifecyclePlan{
		Mode:          "direct",
		ContainerName: "charly-demo",
		EngineBin:     "nerdctl",
		RunArgv:       []string{"nerdctl", "run", "--name", "charly-demo", "img"},
	}
	venue := &recordingVenue{failOn: "'run'"}

	if err := podContainerStart(context.Background(), venue, plan, sel); err == nil {
		t.Fatal("a failing start must be reported")
	}
	if len(venue.calls) != 2 {
		t.Fatalf("cleanup calls: got %v, want the launch then one rm", venue.calls)
	}
	if !strings.Contains(venue.calls[1], "'rm' '-f' 'charly-demo'") {
		t.Errorf("direct cleanup must remove the partial container, got %q", venue.calls[1])
	}
	if strings.Contains(venue.calls[1], "systemctl") {
		t.Errorf("a direct launch has no unit to stop, got %q", venue.calls[1])
	}
}
