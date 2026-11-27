package deploypod

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/opencharly/sdk/deploykit"
	"github.com/opencharly/spec/spec"
)

// installCDIFixture points the derivation at a spec built in a temp dir — one device node and one
// read-only shared-object mount with its SONAME link — and gives the writer a temp data root. It
// restores both after the test, so these arms do not depend on the host having a GPU.
func installCDIFixture(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	versioned := filepath.Join(dir, "libcuda.so.615.78.08")
	if err := os.WriteFile(versioned, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Base(versioned), filepath.Join(dir, "libcuda.so.1")); err != nil {
		t.Fatal(err)
	}
	hostSpec := fmt.Sprintf(`cdiVersion: "0.7.0"
kind: nvidia.com/gpu
devices:
  - name: all
    containerEdits:
      deviceNodes:
        - path: /dev/nvidia0
          major: 195
          minor: 0
      mounts:
        - hostPath: %s
          containerPath: %s
          options: [ro, nosuid, nodev, rbind, rprivate]
`, versioned, versioned)
	specPath := filepath.Join(dir, "nvidia.yaml")
	if err := os.WriteFile(specPath, []byte(hostSpec), 0o644); err != nil {
		t.Fatal(err)
	}
	origSpec := hostCDISpecPath
	hostCDISpecPath = specPath
	t.Cleanup(func() { hostCDISpecPath = origSpec })
	t.Setenv("XDG_DATA_HOME", t.TempDir())
}

// TestKeepIDGPUInputsReachEveryEmitter is the arm that fails if any of the three argv builders — or
// the quadlet config — is left without the derived spec and the read-only library binds. It is
// hermetic: the CDI spec comes from installCDIFixture, not from the host.
func TestKeepIDGPUInputsReachEveryEmitter(t *testing.T) {
	installCDIFixture(t)
	binds := []deploykit.ResolvedBindMount{{Name: "data", HostPath: "/srv/data", ContPath: "/data"}}
	sel := selectionForTest(t, deploykit.Selection{}, "podman")

	// The quadlet config.
	qcfg := deploykit.QuadletConfig{
		BoxName: "box", Instance: "inst", ImageRef: "img", UID: 1000, GID: 1000,
		BindMounts: binds, GPU: true,
	}
	applyKeepIDGPU(&qcfg, false)
	if len(qcfg.PodmanArgs) == 0 || qcfg.PodmanArgs[0] != "--cdi-spec-dir" {
		t.Errorf("quadlet config PodmanArgs = %v, want --cdi-spec-dir", qcfg.PodmanArgs)
	}
	foundRO := false
	for _, b := range qcfg.BindMounts {
		if b.ReadOnly && strings.HasSuffix(b.ContPath, "libcuda.so.1") {
			foundRO = true
		}
	}
	if !foundRO {
		t.Errorf("quadlet config bind mounts = %+v, want a read-only libcuda.so.1 bind", qcfg.BindMounts)
	}
	// The loader path, derived from the SAME binds. A devices-only spec drops the toolkit hooks, so
	// nothing refreshes /etc/ld.so.cache at start; a cache-reading consumer (triton's
	// libcuda_dirs) then cannot find the driver even though the file is bound — measured.
	wantLibPath := "LD_LIBRARY_PATH=" + filepath.Dir(filepath.Join(qcfg.BindMounts[1].ContPath))
	if !hasEnvEntry(qcfg.Env, wantLibPath) {
		t.Errorf("quadlet config env = %v, want %q", qcfg.Env, wantLibPath)
	}

	// The three argv builders.
	directArgv, err := directRunArgs(qcfg, binds, sel)
	if err != nil {
		t.Fatalf("directRunArgs: %v", err)
	}
	startArgv, err := buildStartArgs(sel, "img", 1000, 1000, nil, "ctr", nil, binds,
		true, "", nil, spec.SecurityConfig{}, nil, "/")
	if err != nil {
		t.Fatalf("buildStartArgs: %v", err)
	}
	shellArgv := buildShellArgs(sel.Target, "img", 1000, 1000, nil, nil, binds, true,
		"", "", nil, spec.SecurityConfig{}, "/", true)

	for name, argv := range map[string][]string{
		"directRunArgs":  directArgv,
		"buildStartArgs": startArgv,
		"buildShellArgs": shellArgv,
	} {
		if !hasFlagName(argv, "--cdi-spec-dir") {
			t.Errorf("%s: no --cdi-spec-dir (the stock spec's mounts fail under keep-id): %q", name, argv)
		}
		if !hasReadOnlyBind(argv) {
			t.Errorf("%s: no read-only library bind: %q", name, argv)
		}
		if !hasFlagValue(argv, "-e", wantLibPath) {
			t.Errorf("%s: no loader path %q (a cache-reading consumer cannot find the driver): %q", name, wantLibPath, argv)
		}
	}

	// The control: no GPU device means the stock spec stays, and nothing is added.
	noGPU := deploykit.QuadletConfig{BoxName: "box", ImageRef: "img", UID: 1000, GID: 1000, BindMounts: binds}
	applyKeepIDGPU(&noGPU, false)
	if len(noGPU.PodmanArgs) != 0 || len(noGPU.BindMounts) != 1 {
		t.Errorf("a non-GPU unit must be left untouched: args=%v binds=%+v", noGPU.PodmanArgs, noGPU.BindMounts)
	}
}

// hasEnvEntry reports whether env carries the exact "K=V" entry.
func hasEnvEntry(env []string, want string) bool {
	for _, e := range env {
		if e == want {
			return true
		}
	}
	return false
}

// TestPrependLibraryPathPreservesTheImageValue pins the merge rule: the derived directories go to
// the FRONT and an image-provided LD_LIBRARY_PATH is kept behind them, so a unit does not lose the
// libraries the image itself put on the path.
func TestPrependLibraryPathPreservesTheImageValue(t *testing.T) {
	if got := prependLibraryPath(nil, nil); len(got) != 0 {
		t.Errorf("no directories must leave env untouched, got %v", got)
	}
	got := prependLibraryPath([]string{"PATH=/usr/bin", "LD_LIBRARY_PATH=/opt/lib"}, []string{"/usr/lib"})
	want := []string{"PATH=/usr/bin", "LD_LIBRARY_PATH=/usr/lib:/opt/lib"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("prependLibraryPath = %v, want %v", got, want)
	}
	appended := prependLibraryPath([]string{"PATH=/usr/bin"}, []string{"/usr/lib", "/usr/lib64"})
	if len(appended) != 2 || appended[1] != "LD_LIBRARY_PATH=/usr/lib:/usr/lib64" {
		t.Errorf("prependLibraryPath (no existing entry) = %v", appended)
	}
}
