// volume_root.go — the named-volume ROOT-ownership reconcile.
//
// A named volume's root directory ownership is fixed by the engine at the moment the volume is
// first populated, and never revisited. With podman, populating a VOLUME THAT IS STILL EMPTY mounts
// the image's directory over it (the documented copy-up), so the volume's root takes that
// directory's ownership — mapped through the container's userns. A volume that is already
// NON-EMPTY is passed through exactly as it is, root ownership included. So a volume first
// populated from an image revision whose directory was root-owned keeps a root-owned root for
// every later deploy of that (deploy, volume) pair, even after the image is corrected: the
// contents are correctly 1000-owned while the root is not, and a runtime user of uid 1000 cannot
// create its own files there.
//
// Measured on a disposable R10 bed: `charly check run check-githubrunner-pod` attached a volume
// populated weeks earlier from an image whose actions-runner directory was root-owned, and the
// runner's config.sh died with `Access to the path '/home/user/actions-runner/_diag' is denied`
// (exit 134), while the same image against a FRESH volume of the same path came out correct — which
// is why this cannot be fixed in the image alone, and why the deploy path must reconcile the root
// on every config rather than rely on the engine's one-shot creation. A deploy that re-runs is
// exactly the normal case: `charly remove` deliberately keeps named volumes (its `--purge` flag is
// spelled "Also remove named volumes"), so a bed is not hermetic after a failed or aborted run.
package deploypod

import (
	"context"
	"fmt"

	"github.com/opencharly/sdk"
	"github.com/opencharly/sdk/deploykit"
	"github.com/opencharly/sdk/kit"
)

// volumeRootScratchPath is where the reconcile mounts the volume inside its throwaway container.
// It is deliberately NOT the volume's real container path: mounting an EMPTY named volume at a path
// that exists in the image triggers the engine's copy-up, so a repair that is meant to change
// nothing but the root's OWNERSHIP would populate the volume as a side effect. The scratch path
// exists in no image, so the mount stays empty and the mount point — the volume's root directory —
// is the only thing this helper touches.
const volumeRootScratchPath = "/charly-volume-root"

// Three different emitters can create the container a deploy's named volumes are attached to, and
// each guards its own keep-id flag. These are their conditions, each named for its emitter; the two
// that live in this package ARE the emitters' conditions (called from the argv builder itself), so
// they cannot drift, and the third — sdk/deploykit/quadlet.go's `UserNS=` line, which cannot import
// this package — is mirrored here and pinned by a test.

// startArgsKeepID is buildStartArgs' condition, shared with buildShellArgs (the `charly shell` plan,
// resolve_f12.go's resolvePodShellPlan): the resolved engine decides, and only podman has keep-id.
func startArgsKeepID(engine string, bindMounts int) bool {
	return engine == "podman" && bindMounts > 0
}

// directPodmanKeepID is directPodmanArgs' condition. The direct path never consults the resolved
// engine: runConfigDirect execs `podman` itself (`exec.Command("podman", ...)`,
// config_setup_helpers.go), so the engine cannot vary here and the condition tests the deploy user
// instead.
func directPodmanKeepID(uid, bindMounts int) bool {
	return uid > 0 && bindMounts > 0
}

// quadletKeepID mirrors sdk/deploykit/quadlet.go's `UserNS=keep-id:uid=…,gid=…` line: a quadlet
// deploy is podman-only, so the emitter tests neither the engine nor the deploy user, only whether
// any bind mount exists.
func quadletKeepID(bindMounts int) bool {
	return bindMounts > 0
}

// volumeRootKeepID reports whether this deploy's runtime container is emitted with
// `--userns=keep-id:uid=<uid>,gid=<gid>`, by ANY emitter that could create it. Under keep-id the
// deploy user's host UID is mapped to the in-container UID, so the user who owns the named volume's
// root IS the runtime user and the root is writable as it stands; the reconcile cannot improve on
// that from its own standard-mapping helper — where that same host user appears as container root —
// so it steps aside rather than write an owner the keep-id runtime could not use.
//
// The answer is deliberately an OVER-approximation (R3: one predicate, one answer per mode): in
// direct mode two builders can create the container — `charly config` runs directPodmanArgs and
// `charly start` runs buildStartArgs — so standing aside when EITHER would emit keep-id is the safe
// side. The cost of the over-approximation is only that such a deploy keeps the stale root this
// reconcile exists to repair; the cost of guessing the other way is an owner the live container
// cannot write as, which is the failure being fixed.
func volumeRootKeepID(runMode, engine string, uid, bindMounts int) bool {
	if runMode == "direct" {
		return directPodmanKeepID(uid, bindMounts) || startArgsKeepID(engine, bindMounts)
	}
	return quadletKeepID(bindMounts)
}

// volumeRootReconcileArgs builds the argv of the throwaway container that gives one named volume a
// root directory the runtime user can write into. It runs as the container's root — under the
// rootless mapping that is the host user who owns the volume — because the image's own user (e.g.
// uid 1000) does not own the volume root and so cannot chown it.
//
// The command is `chown <uid>:<gid> <scratch>`, run through `--entrypoint chown`: the volume root is
// given to the deploy's RUNTIME USER directly. That is the property the deploy needs — that user
// must be able to create its own files there — and the one the measured failure lacked. It is
// deliberately NOT "copy the image directory's owner onto the root": that would reproduce the very
// state being repaired whenever the image's directory is itself root-owned, which is how the
// measured failure arose.
//
// Nothing here is passed through a shell, and that is a property of the shape of the command, not a
// quoting discipline that could be got wrong later: the argv IS the command, `chown` is the
// container's entrypoint, and its operands (`<uid>:<gid>` from two ints, and the constant scratch
// path) are the only things it is ever given. A volume name or image reference carrying `$`, a
// backtick or any other metacharacter has no shell in this container to interpret it. `--entrypoint`
// also makes the command explicit: a deploy image may define an ENTRYPOINT of its own, which would
// otherwise swallow these arguments.
//
// The one requirement on the image is therefore a `chown` — a POSIX utility present in every
// standard distribution image and in BusyBox. No shell and no GNU `--reference` are needed.
func volumeRootReconcileArgs(engine, imageRef string, uid, gid int, volumeName string) []string {
	return []string{
		kit.EngineBinary(engine), "run", "--rm",
		"--user", "0",
		"-v", volumeName + ":" + volumeRootScratchPath,
		"--entrypoint", "chown",
		imageRef, fmt.Sprintf("%d:%d", uid, gid), volumeRootScratchPath,
	}
}

// reconcileNamedVolumeRoots gives every named volume of this deploy a root directory owned by the
// deploy's runtime user, before any container is created — so the ownership is in place when the
// deploy's own `-v`/`Volume=` attach happens, whether the volume is fresh (this helper creates it)
// or already populated (the case that fails today). It runs over the served host executor; the
// plugin walks no host itself.
//
// A failure is fatal on purpose, and that is a deliberate behaviour change: an image that ships no
// `chown` now fails HERE, at `charly config`, instead of failing later as the application's own
// crash far from its cause (the measured exit=134) — or, worse, silently handing the application a
// volume the runtime user cannot write into. Failing at the point where the ownership could not be
// established is the honest outcome; the requirement it states (a POSIX `chown` in the image) is
// smaller than the one the alternative would need, since it asks for neither a shell nor a GNU
// `chown`.
func reconcileNamedVolumeRoots(ctx context.Context, ex *sdk.Executor, engine string, keepID bool, uid, gid int, imageRef string, volumes []deploykit.VolumeMount) error {
	if keepID || len(volumes) == 0 {
		return nil
	}
	for _, vol := range volumes {
		argv := volumeRootReconcileArgs(engine, imageRef, uid, gid, vol.VolumeName)
		if err := ex.VenueRunSilent(ctx, shellJoin(argv)); err != nil {
			return fmt.Errorf("plugin-deploy-pod reconcile volume root (%s): %w", vol.VolumeName, err)
		}
	}
	return nil
}
