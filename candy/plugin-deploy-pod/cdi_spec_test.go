package deploypod

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDeriveDevicesOnlyCDI pins the derivation against a spec built in a temp dir, so it does not
// depend on the host's own /usr/lib layout:
//
//   - the deviceNodes are kept (top-level v0.7 edits AND the `all` device's own, deduped);
//   - the mounts and hooks are dropped;
//   - only read-only shared objects become binds — the MPS control binary is left out;
//   - a VERSIONED library mount also gets a bind for each host SONAME link that resolves to it,
//     because the dropped `create-symlinks` hook is what would have made `libcuda.so.1` exist.
func TestDeriveDevicesOnlyCDI(t *testing.T) {
	libDir := t.TempDir()
	versioned := filepath.Join(libDir, "libcuda.so.615.78.08")
	if err := os.WriteFile(versioned, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Base(versioned), filepath.Join(libDir, "libcuda.so.1")); err != nil {
		t.Fatal(err)
	}
	topDir := t.TempDir()
	topVersioned := filepath.Join(topDir, "libnvidia-ptxjitcompiler.so.615.78.08")
	if err := os.WriteFile(topVersioned, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Base(topVersioned), filepath.Join(topDir, "libnvidia-ptxjitcompiler.so.1")); err != nil {
		t.Fatal(err)
	}

	hostSpec := fmt.Sprintf(`cdiVersion: "0.7.0"
kind: nvidia.com/gpu
devices:
  - name: "0"
    containerEdits:
      deviceNodes:
        - path: /dev/nvidia0
  - name: all
    containerEdits:
      deviceNodes:
        - path: /dev/nvidiactl
      mounts:
        - hostPath: %[1]s
          containerPath: %[1]s
          options: [ro, nosuid, nodev, rbind, rprivate]
        - hostPath: /usr/bin/nvidia-cuda-mps-control
          containerPath: /usr/bin/nvidia-cuda-mps-control
          options: [ro, nosuid, nodev, rbind, rprivate]
containerEdits:
  hooks:
    - hookName: createContainer
      path: /usr/bin/nvidia-cdi-hook
  deviceNodes:
    - path: /dev/nvidia-caps/nvidia-cap1
  mounts:
    - hostPath: %[2]s
      containerPath: %[2]s
      options: [ro, nosuid, nodev, rbind, rprivate]
`, versioned, topVersioned)

	path := filepath.Join(t.TempDir(), "nvidia.yaml")
	if err := os.WriteFile(path, []byte(hostSpec), 0o644); err != nil {
		t.Fatal(err)
	}

	derived, libs, err := deriveDevicesOnlyCDI(path)
	if err != nil {
		t.Fatalf("deriveDevicesOnlyCDI: %v", err)
	}
	got := string(derived)
	if strings.Contains(got, "mounts:") {
		t.Errorf("derived spec still carries mounts:\n%s", got)
	}
	if strings.Contains(got, "hooks:") {
		t.Errorf("derived spec still carries hooks:\n%s", got)
	}
	for _, want := range []string{"/dev/nvidiactl", "/dev/nvidia-caps/nvidia-cap1", "kind: nvidia.com/gpu"} {
		if !strings.Contains(got, want) {
			t.Errorf("derived spec missing %q:\n%s", want, got)
		}
	}

	// The two library mounts (one per-device, one top-level), each read-only, plus the one SONAME
	// bind the temp dir's symlink yields. The MPS binary is a mount but not a shared object.
	var paths []string
	for _, l := range libs {
		if !l.ReadOnly {
			t.Errorf("%s is not read-only", l.ContPath)
		}
		if strings.Contains(l.ContPath, "mps") {
			t.Errorf("%s must not be bound: it is not a shared object", l.ContPath)
		}
		paths = append(paths, l.ContPath)
	}
	wantPaths := map[string]bool{
		versioned:                             true,
		filepath.Join(libDir, "libcuda.so.1"): true,
		topVersioned:                          true,
		filepath.Join(topDir, "libnvidia-ptxjitcompiler.so.1"): true,
	}
	for p := range wantPaths {
		found := false
		for _, got := range paths {
			if got == p {
				found = true
			}
		}
		if !found {
			t.Errorf("no bind for %q; got %v", p, paths)
		}
	}
	if len(libs) != len(wantPaths) {
		t.Errorf("got %d binds (%v), want %d", len(libs), paths, len(wantPaths))
	}
}
