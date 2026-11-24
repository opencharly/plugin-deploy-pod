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
	}

	// The control: no GPU device means the stock spec stays, and nothing is added.
	noGPU := deploykit.QuadletConfig{BoxName: "box", ImageRef: "img", UID: 1000, GID: 1000, BindMounts: binds}
	applyKeepIDGPU(&noGPU, false)
	if len(noGPU.PodmanArgs) != 0 || len(noGPU.BindMounts) != 1 {
		t.Errorf("a non-GPU unit must be left untouched: args=%v binds=%+v", noGPU.PodmanArgs, noGPU.BindMounts)
	}
}
