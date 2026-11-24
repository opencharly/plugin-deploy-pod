package deploypod

import (
	"context"
	"strings"
	"testing"

	"github.com/opencharly/sdk/deploykit"
	"github.com/opencharly/sdk/vmshared"
)

// volume_root_test.go — guards the named-volume root reconcile (volume_root.go).
//
// The behaviour under test is the one measured failing on a disposable R10 bed: a named volume first
// populated from an image whose directory was root-owned keeps a root-owned root on every later
// deploy (the engine only sets a named volume's root when it populates an EMPTY one), so a runtime
// user of uid 1000 cannot write into its own volume — the runner's config.sh died with
// `Access to the path '/home/user/actions-runner/_diag' is denied` (exit 134). These tests pin the
// argv that repairs it, the injection-safety property of that argv, and the predicate that decides
// when the repair must NOT run.

// hasKeepID reports whether an argv carries a keep-id userns flag, in either spelling
// (`--userns=keep-id:…`, which this package emits, and `--userns keep-id:…`).
func hasKeepID(args []string) bool {
	return strings.Contains(shellJoin(args), "keep-id")
}

// hasToken reports whether the argv carries an exact token.
func hasToken(args []string, tok string) bool {
	for _, a := range args {
		if a == tok {
			return true
		}
	}
	return false
}

// TestVolumeRootReconcileArgs pins the reconcile argv: the deploy image, run as the container's
// root (the image's own user does not own the volume root and cannot chown it), with the volume
// mounted at the SCRATCH path — never at the volume's real container path, where mounting an empty
// volume would trigger the engine's copy-up and populate it as a side effect of a repair — and the
// volume root given to the deploy's own runtime user.
func TestVolumeRootReconcileArgs(t *testing.T) {
	vol := deploykit.VolumeMount{VolumeName: "charly-githubrunner-state", ContainerPath: "/home/user/actions-runner"}

	args := volumeRootReconcileArgs("podman", "charly-runner:2026.278.2123", 1000, 1000, vol.VolumeName)

	want := []string{
		"podman", "run", "--rm",
		"--user", "0",
		"-v", "charly-githubrunner-state:/charly-volume-root",
		"--entrypoint", "chown",
		"charly-runner:2026.278.2123", "1000:1000", "/charly-volume-root",
	}
	if len(args) != len(want) {
		t.Fatalf("argv length: got %d (%q), want %d (%q)", len(args), args, len(want), want)
	}
	for i := range want {
		if args[i] != want[i] {
			t.Errorf("argv[%d]: got %q, want %q", i, args[i], want[i])
		}
	}

	// The mount target must be the scratch path, and never the volume's real container path:
	// mounting an EMPTY volume where the image has a directory would set off copy-up and populate
	// the volume, which this helper must not do.
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

	// The root is given to the deploy's own runtime user, as two ints — not to the image
	// directory's owner, which is root-owned in exactly the state being repaired.
	if !hasToken(args, "1000:1000") {
		t.Errorf("reconcile must chown the root to the runtime user, got %q", args)
	}

	// There is no shell: the chown IS the container's entrypoint, so no operand can be interpreted
	// as shell syntax (T3).
	if hasToken(args, "sh") || hasToken(args, "-c") || !hasToken(args, "chown") {
		t.Errorf("reconcile must run chown directly, with no shell: %q", args)
	}

	// A non-root runtime user gets its own ids, not a hardcoded 1000:1000.
	other := volumeRootReconcileArgs("docker", "img", 1001, 1002, "v")
	if got := other[0]; got != "docker" {
		t.Errorf("engine binary: got %q, want %q", got, "docker")
	}
	if !hasToken(other, "1001:1002") {
		t.Errorf("chown must use the deploy's own uid:gid, got %q", other)
	}
}

// TestVolumeRootReconcileArgsIsShellSafe pins the property the reviewer raised (T3/R4): a volume's
// ContainerPath must never reach a shell, however it is spelled. The reconcile builds its command
// from an int pair and a package constant, and the path is not among the operands at all — so a
// metacharacter in the path cannot be substituted by anything, in this container or on the host.
// The host side is covered too: shellJoin quotes every token with shellquote.ShellQuote.
func TestVolumeRootReconcileArgsIsShellSafe(t *testing.T) {
	payloads := []string{
		"/x$(touch /tmp/pwned)",
		"/x`touch /tmp/pwned`",
		"/x; touch /tmp/pwned",
		`/x"; touch /tmp/pwned; "`,
		"/x${IFS}touch",
		"/x\n touch /tmp/pwned",
	}
	for _, cpath := range payloads {
		vol := deploykit.VolumeMount{VolumeName: "charly-x-state", ContainerPath: cpath}
		args := volumeRootReconcileArgs("podman", "img:tag", 1000, 1000, vol.VolumeName)

		for i, a := range args {
			if strings.Contains(a, cpath) {
				t.Errorf("payload %q reached argv[%d]=%q", cpath, i, a)
			}
		}
		if joined := shellJoin(args); strings.Contains(joined, cpath) {
			t.Errorf("payload %q reached the host command line: %q", cpath, joined)
		}
		// The injection has no surface to begin with: the only operand that is not a constant is the
		// numeric uid:gid pair.
		if hasToken(args, "sh") || hasToken(args, "-c") {
			t.Errorf("payload %q: reconcile must not invoke a shell: %q", cpath, args)
		}
		if !hasToken(args, "1000:1000") {
			t.Errorf("payload %q: the deploy user's ids must be the numeric operand: %q", cpath, args)
		}
	}
}

// TestVolumeRootKeepIDCoversEveryEmitter is the drift guard. Three emitters can create the
// container a deploy's named volumes are attached to — GenerateQuadlet (the quadlet path),
// directRunArgs (`charly config` in direct mode) and buildStartArgs (`charly start` in direct
// mode) — and the reconcile must stand aside in EXACTLY the deployments where one of them emits
// keep-id: under keep-id the volume's owner IS the runtime user, and the reconcile's
// standard-mapping helper could only write an owner that runtime could not use.
//
// The assertion is equality against the emitters' own output, not against a retyped table, so a
// condition that moves on either side fails here: the argv builders ask podUserNSFor (the
// resolved-deploy decision over the capability facts) directly, and the quadlet emitter lives in
// sdk (which cannot import this package) so its mirror is compared against the renderer's real
// output. Nothing is left to drift.
//
// The GPU rows are the non-root half: a deploy that carries the CDI device emits keep-id from EVERY
// emitter — the device rides keep-id with a devices-only CDI spec, so the volume root reconcile
// still applies.
func TestVolumeRootKeepIDCoversEveryEmitter(t *testing.T) {
	for _, tc := range []struct {
		name, engine string
		uid, binds   int
		gpu, priv    bool
	}{
		{name: "podman, runtime user, no bind mounts", engine: "podman", uid: 1000, binds: 0},
		{name: "podman, runtime user, bind mounts", engine: "podman", uid: 1000, binds: 1},
		{name: "podman, container root, bind mounts", engine: "podman", uid: 0, binds: 1},
		{name: "docker, runtime user, bind mounts", engine: "docker", uid: 1000, binds: 1},
		{name: "containerd, runtime user, bind mounts", engine: "containerd", uid: 1000, binds: 1},
		{name: "docker, container root, bind mounts", engine: "docker", uid: 0, binds: 1},
		// The CDI-device rows: every emitter must emit keep-id.
		{name: "podman, cdi device, bind mounts", engine: "podman", uid: 1000, binds: 1, gpu: true},
		{name: "podman, cdi device, no bind mounts", engine: "podman", uid: 1000, binds: 0, gpu: true},
		// A privileged unit gets every device through --privileged and never receives a CDI
		// mount, so it keeps the mapping it had.
		{name: "podman, privileged gpu, bind mounts", engine: "podman", uid: 1000, binds: 1, gpu: true, priv: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			binds := make([]deploykit.ResolvedBindMount, tc.binds)
			qcfg := deploykit.QuadletConfig{
				BoxName: "box", Instance: "inst", ImageRef: "img",
				UID: tc.uid, GID: tc.uid, BindMounts: binds, GPU: tc.gpu,
				Security: vmshared.SecurityConfig{Privileged: tc.priv},
			}

			quadletEmits := strings.Contains(deploykit.GenerateQuadlet(qcfg), "UserNS=keep-id")
			// A QUADLET run is podman by construction, so the renderer's answer is comparable only
			// on the podman rows — but on those it must be the SAME answer the argv emitters give,
			// for every row including the privileged one.
			if tc.engine == "podman" {
				if got := volumeRootKeepID(tc.engine, tc.binds); got != quadletEmits {
					t.Errorf("quadlet: volumeRootKeepID = %v but GenerateQuadlet emits keep-id = %v", got, quadletEmits)
				}
			}

			// Direct mode has TWO container creators, and either may be the one that runs.
			// Both build through the engine start_plan body, so both ask podUserNSFor.
			sel := selectionForTest(t, deploykit.Selection{}, tc.engine)
			directArgv, err := directRunArgs(qcfg, binds, sel)
			if err != nil {
				t.Fatalf("directRunArgs(%s): %v", tc.engine, err)
			}
			directEmits := hasKeepID(directArgv)
			startArgv, err := buildStartArgs(sel, "img", tc.uid, tc.uid, nil, "ctr", nil, binds,
				tc.gpu, "", nil, vmshared.SecurityConfig{Privileged: tc.priv}, nil, "/")
			if err != nil {
				t.Fatalf("buildStartArgs(%s): %v", tc.engine, err)
			}
			startEmits := hasKeepID(startArgv)
			if want := directEmits || startEmits; volumeRootKeepID(tc.engine, tc.binds) != want {
				t.Errorf("volumeRootKeepID = %v but directRunArgs emits keep-id = %v and buildStartArgs = %v",
					volumeRootKeepID(tc.engine, tc.binds), directEmits, startEmits)
			}

			// buildShellArgs is the transient `charly run` twin of buildStartArgs and attaches the
			// same named volumes; it carries the same condition, so it must agree with it.
			shell := hasKeepID(buildShellArgs(sel.Target, "img", tc.uid, tc.uid, nil, nil, binds,
				tc.gpu, "", "", nil, vmshared.SecurityConfig{Privileged: tc.priv}, "/", true))
			if shell != startEmits {
				t.Errorf("buildShellArgs emits keep-id = %v but buildStartArgs = %v", shell, startEmits)
			}

			// THE CROSS-EMITTER AGREEMENT, on EVERY row and with no skip: the three argv builders
			// must answer the keep-id question identically, and on a podman row the quadlet
			// renderer must answer it the same way too. This is the assertion a divergent
			// predicate cannot survive — a skipped row is how the privileged divergence shipped.
			if directEmits != startEmits || shell != startEmits {
				t.Errorf("argv emitters disagree on keep-id: directRunArgs=%v buildStartArgs=%v buildShellArgs=%v",
					directEmits, startEmits, shell)
			}
			if tc.engine == "podman" && quadletEmits != startEmits {
				t.Errorf("argv and quadlet disagree on keep-id: GenerateQuadlet=%v buildStartArgs=%v",
					quadletEmits, startEmits)
			}
			// And a deploy that carries the CDI device is emitted WITH the keep-id mapping by
			// EVERY emitter: no charly pod runs as container root, and the device rides keep-id
			// once the engine is pointed at a devices-only spec (cdi_spec.go).
			if tc.gpu && !tc.priv && tc.binds > 0 {
				for _, e := range []struct {
					name string
					got  bool
				}{
					{"GenerateQuadlet", quadletEmits},
					{"directRunArgs", directEmits},
					{"buildStartArgs", startEmits},
					{"buildShellArgs", shell},
				} {
					if !e.got {
						t.Errorf("%s must emit keep-id for a deploy that carries the CDI device", e.name)
					}
				}
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
