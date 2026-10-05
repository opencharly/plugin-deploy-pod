package deploypod

import (
	"context"
	"strings"
	"testing"

	"github.com/opencharly/sdk/deploykit"
	"github.com/opencharly/spec/spec"
)

// volume_root_test.go — guards the named-volume root reconcile (volume_root.go).
//
// The behaviour under test is the one measured failing on a disposable R10 bed: a named volume first
// populated from an image whose directory was root-owned keeps a root-owned root on every later
// deploy (the engine only sets a named volume's root when it populates an EMPTY one), so a runtime
// user of uid 1000 cannot write into its own volume — the runner's config.sh died with
// `Access to the path '/home/user/actions-runner/_diag' is denied` (exit 134). These tests pin the
// argv that repairs it, and pin the predicate that decides when the repair must NOT run.

// hasKeepID reports whether an argv carries a keep-id userns flag, in either spelling
// (`--userns=keep-id:…`, which this package emits, and `--userns keep-id:…`).
func hasKeepID(args []string) bool {
	return strings.Contains(shellJoin(args), "keep-id")
}

// TestVolumeRootReconcileArgs pins the reconcile argv: the deploy image, run as the container's
// root (the image's own user does not own the volume root and cannot chown it), with the volume
// mounted at the SCRATCH path — never at the volume's real container path, which would mask the
// image's directory whose ownership is the reference — and a POSIX shell made explicit, because a
// deploy image may define an ENTRYPOINT that would otherwise swallow the -c argument.
func TestVolumeRootReconcileArgs(t *testing.T) {
	vol := deploykit.VolumeMount{VolumeName: "charly-githubrunner-state", ContainerPath: "/home/user/actions-runner"}

	args := volumeRootReconcileArgs("podman", "charly-runner:2026.278.2123", 1000, 1000, vol)

	want := []string{
		"podman", "run", "--rm",
		"--user", "0",
		"-v", "charly-githubrunner-state:/charly-volume-root",
		"--entrypoint", "sh",
		"charly-runner:2026.278.2123", "-c",
		`if [ -e "/home/user/actions-runner" ]; then chown --reference="/home/user/actions-runner" "/charly-volume-root"; else chown 1000:1000 "/charly-volume-root"; fi`,
	}
	if len(args) != len(want) {
		t.Fatalf("argv length: got %d (%q), want %d (%q)", len(args), args, len(want), want)
	}
	for i := range want {
		if args[i] != want[i] {
			t.Errorf("argv[%d]: got %q, want %q", i, args[i], want[i])
		}
	}

	// The mount target must be the scratch path. Mounting the volume at its real container path
	// would hide the image's directory, leaving nothing to mirror onto the root.
	var mount string
	for i, a := range args {
		if a == "-v" {
			mount = args[i+1]
		}
	}
	if _, target, ok := strings.Cut(mount, ":"); !ok || target != volumeRootScratchPath {
		t.Errorf("volume mounted at %q, want the scratch path %q", mount, volumeRootScratchPath)
	}
	if strings.Contains(mount, ":"+vol.ContainerPath) {
		t.Errorf("volume must not be mounted at its real container path %q: %q", vol.ContainerPath, mount)
	}
	if !strings.Contains(args[len(args)-1], "--reference=") {
		t.Errorf("reconcile must mirror the image directory's ownership, got %q", args[len(args)-1])
	}

	// A non-root runtime user gets the fallback ids, not a hardcoded 1000:1000.
	other := volumeRootReconcileArgs("docker", "img", 1001, 1002,
		deploykit.VolumeMount{VolumeName: "v", ContainerPath: "/missing"})
	if got := other[0]; got != "docker" {
		t.Errorf("engine binary: got %q, want %q", got, "docker")
	}
	if last := other[len(other)-1]; !strings.Contains(last, "chown 1001:1002") {
		t.Errorf("fallback chown must use the deploy's own uid:gid, got %q", last)
	}
}

// TestVolumeRootKeepIDCoversEveryEmitter is the drift guard. Three emitters can create the
// container a deploy's named volumes are attached to — GenerateQuadlet (the quadlet path),
// directPodmanArgs (`charly config` in direct mode, which execs podman whatever engine the runtime
// resolved to) and buildStartArgs (`charly start` in direct mode, which respects the engine) — and
// the reconcile must stand aside in EXACTLY the deployments where one of them emits keep-id: under
// keep-id the volume's owner IS the runtime user, and the reconcile's standard-mapping helper could
// only write an owner that runtime could not use.
//
// The assertion is equality against the emitters' own output, not against a retyped table, so a
// condition that moves on either side fails here: the two argv builders call this package's
// predicates directly, and the quadlet emitter lives in sdk (which cannot import this package) so
// its mirror is compared against the renderer's real output. Nothing is left to drift.
func TestVolumeRootKeepIDCoversEveryEmitter(t *testing.T) {
	for _, tc := range []struct {
		name, engine string
		uid, binds   int
	}{
		{"podman, runtime user, no bind mounts", "podman", 1000, 0},
		{"podman, runtime user, bind mounts", "podman", 1000, 1},
		{"podman, container root, bind mounts", "podman", 0, 1},
		{"docker, runtime user, bind mounts", "docker", 1000, 1},
		{"nerdctl, runtime user, bind mounts", "nerdctl", 1000, 1},
		{"docker, container root, bind mounts", "docker", 0, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			binds := make([]deploykit.ResolvedBindMount, tc.binds)
			qcfg := deploykit.QuadletConfig{
				BoxName: "box", Instance: "inst", ImageRef: "img",
				UID: tc.uid, GID: tc.uid, BindMounts: binds,
			}

			quadletEmits := strings.Contains(deploykit.GenerateQuadlet(qcfg), "UserNS=keep-id")
			if got := volumeRootKeepID("quadlet", tc.engine, tc.uid, tc.binds); got != quadletEmits {
				t.Errorf("quadlet mode: volumeRootKeepID = %v but GenerateQuadlet emits keep-id = %v", got, quadletEmits)
			}

			// Direct mode has TWO container creators, and either may be the one that runs.
			directEmits := hasKeepID(directPodmanArgs(qcfg, binds))
			startEmits := hasKeepID(buildStartArgs(tc.engine, "img", tc.uid, tc.uid, nil, "ctr", nil, binds,
				false, "", nil, spec.SecurityConfig{}, nil, "/"))
			if want := directEmits || startEmits; volumeRootKeepID("direct", tc.engine, tc.uid, tc.binds) != want {
				t.Errorf("direct mode: volumeRootKeepID = %v but directPodmanArgs emits keep-id = %v and buildStartArgs = %v",
					volumeRootKeepID("direct", tc.engine, tc.uid, tc.binds), directEmits, startEmits)
			}

			// buildShellArgs is the transient `charly run` twin of buildStartArgs and attaches the
			// same named volumes; it carries the same condition, so it must agree with it.
			shell := hasKeepID(buildShellArgs(tc.engine, "img", tc.uid, tc.uid, nil, nil, binds,
				false, "", "", nil, spec.SecurityConfig{}, "/", true))
			if shell != startEmits {
				t.Errorf("buildShellArgs emits keep-id = %v but buildStartArgs = %v", shell, startEmits)
			}
		})
	}
}

// TestReconcileNamedVolumeRootsSkips pins the two no-op cases. A nil executor is the proof that
// nothing runs: any attempt to exec a command on it would panic instead of returning nil.
func TestReconcileNamedVolumeRootsSkips(t *testing.T) {
	vols := []deploykit.VolumeMount{{VolumeName: "charly-x-state", ContainerPath: "/p"}}

	if err := reconcileNamedVolumeRoots(context.Background(), nil, "podman", true, 1000, 1000, "img", vols); err != nil {
		t.Errorf("keep-id deploys must not be reconciled: %v", err)
	}
	if err := reconcileNamedVolumeRoots(context.Background(), nil, "podman", false, 1000, 1000, "img", nil); err != nil {
		t.Errorf("a deploy with no named volumes must run nothing: %v", err)
	}
}
