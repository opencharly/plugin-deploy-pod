package deploypod

// cdi_spec.go — the keep-id half of the GPU injection.
//
// A charly pod runs as its image user, and a unit with a bind mount runs under keep-id. A CDI
// device rides that unit normally; what crun cannot apply inside the keep-id user namespace is the
// CDI spec's `mounts` list (each mount is applied with mount_setattr(..., MOUNT_ATTR_IDMAP), which
// needs CAP_SYS_ADMIN in the user namespace that owns the file's superblock — the initial namespace
// for host files), failing at the first entry with `mount_setattr <path>: Operation not permitted`,
// exit 126. The device nodes need no such mount.
//
// So the engine is pointed at a DEVICES-ONLY spec derived from the host's own: the deviceNodes are
// kept, the mounts and hooks are dropped, and the shared objects the spec would have mounted (the
// driver user-space libraries) are appended to the unit as read-only binds to the same container
// paths. The result is regenerated on every `charly config`, so a host driver update is picked up
// by the same command that rewrites the unit.

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/opencharly/sdk/deploykit"
	"github.com/opencharly/spec/storeroot"
)

// hostCDISpecPath is the toolkit's default NVIDIA spec. A host with a different one can still be
// served: the derivation reads whatever `nvidia-ctk` wrote, and an absent file simply means there
// is nothing to derive (the caller then leaves the unit as it was). A variable, not a constant, so
// a test can point it at a fixture instead of depending on the host's GPU.
var hostCDISpecPath = "/etc/cdi/nvidia.yaml"

type cdiSpecFile struct {
	CDIVersion string       `yaml:"cdiVersion"`
	Kind       string       `yaml:"kind"`
	Devices    []cdiSpecDev `yaml:"devices"`
	// ContainerEdits is the CDI v0.7 top-level form: edits that apply to EVERY device. nvidia-ctk
	// puts the driver libraries, the environment and the hooks here, so a derivation that reads
	// only the per-device edits sees the device nodes and none of the mounts.
	ContainerEdits cdiSpecEdit `yaml:"containerEdits,omitempty"`
}

type cdiSpecDev struct {
	Name           string      `yaml:"name"`
	ContainerEdits cdiSpecEdit `yaml:"containerEdits"`
}

type cdiSpecEdit struct {
	DeviceNodes []cdiDeviceNode `yaml:"deviceNodes,omitempty"`
	Mounts      []cdiMount      `yaml:"mounts,omitempty"`
}

type cdiDeviceNode struct {
	Path string `yaml:"path"`
	// No omitempty on the numbers: a node CAN have minor 0 (/dev/nvidia0), and emitting it as
	// absent is a latent difference from the host's own spec.
	Major       int64  `yaml:"major"`
	Minor       int64  `yaml:"minor"`
	FileMode    *int   `yaml:"fileMode,omitempty"`
	Permissions string `yaml:"permissions,omitempty"`
	UID         *int   `yaml:"uid,omitempty"`
	GID         *int   `yaml:"gid,omitempty"`
}

type cdiMount struct {
	HostPath      string   `yaml:"hostPath"`
	ContainerPath string   `yaml:"containerPath"`
	Options       []string `yaml:"options,omitempty"`
}

// deriveDevicesOnlyCDI reads the host's CDI spec and returns the devices-only YAML to write plus the
// driver-library binds that replace the spec's mounts.
//
// A mount becomes a bind only when it is already read-only in the spec and its container path is a
// shared object — the driver libraries (libcuda.so.<ver> at libcuda.so.1, libnvidia-ml, …). The
// toolkit's non-library mounts (the MPS control/server binaries, for instance) stay out: they are
// not needed to run a workload and they are what fails first.
func deriveDevicesOnlyCDI(hostSpecPath string) ([]byte, []deploykit.ResolvedBindMount, error) {
	raw, err := os.ReadFile(hostSpecPath)
	if err != nil {
		return nil, nil, err
	}
	var host cdiSpecFile
	if err := yaml.Unmarshal(raw, &host); err != nil {
		return nil, nil, fmt.Errorf("parsing %s: %w", hostSpecPath, err)
	}
	if len(host.Devices) == 0 {
		return nil, nil, fmt.Errorf("%s declares no devices", hostSpecPath)
	}
	// The engine asks for the spec's "all" device; fall back to the first for a spec that names
	// its devices differently.
	dev := host.Devices[0]
	for _, d := range host.Devices {
		if d.Name == "all" {
			dev = d
			break
		}
	}

	// Device nodes: the top-level edits and the device's own, deduped by path (the v0.7 spec
	// repeats the same nodes in both places on some toolkit builds).
	seen := map[string]bool{}
	var nodes []cdiDeviceNode
	for _, n := range append(append([]cdiDeviceNode{}, host.ContainerEdits.DeviceNodes...), dev.ContainerEdits.DeviceNodes...) {
		if n.Path == "" || seen[n.Path] {
			continue
		}
		seen[n.Path] = true
		nodes = append(nodes, n)
	}
	if len(nodes) == 0 {
		return nil, nil, fmt.Errorf("%s device %q declares no deviceNodes", hostSpecPath, dev.Name)
	}

	var libs []deploykit.ResolvedBindMount
	for _, m := range append(append([]cdiMount{}, host.ContainerEdits.Mounts...), dev.ContainerEdits.Mounts...) {
		if m.HostPath == "" || m.ContainerPath == "" || !mountIsReadOnly(m.Options) {
			continue
		}
		if !strings.Contains(filepath.Base(m.ContainerPath), ".so") {
			continue
		}
		libs = append(libs, deploykit.ResolvedBindMount{
			Name:     strings.TrimPrefix(filepath.Base(m.ContainerPath), "lib"),
			HostPath: m.HostPath, ContPath: m.ContainerPath, ReadOnly: true,
		})
		libs = append(libs, sonameBinds(m.HostPath, m.ContainerPath)...)
	}

	derived := cdiSpecFile{
		CDIVersion: "0.5.0",
		Kind:       host.Kind,
		Devices: []cdiSpecDev{{
			Name:           "all",
			ContainerEdits: cdiSpecEdit{DeviceNodes: nodes},
		}},
	}
	out, err := yaml.Marshal(derived)
	if err != nil {
		return nil, nil, fmt.Errorf("marshalling the derived CDI spec: %w", err)
	}
	return out, libs, nil
}

// sonameBinds returns the read-only binds that recreate the SONAME links the toolkit's
// `create-symlinks` hook would have made: the spec mounts the VERSIONED file (libcuda.so.615.78.08
// at libcuda.so.615.78.08), and the dynamic loader asks for libcuda.so.1. The hook cannot be relied
// on here — the derived spec drops hooks — so each host symlink that resolves to the mounted file is
// bound as `<the host SONAME symlink> -> <that same path in the container>`: the SOURCE is the
// symlink, so a driver upgrade that replaces the versioned file cannot leave a dangling bind.
func sonameBinds(hostPath, contPath string) []deploykit.ResolvedBindMount {
	dir, base := filepath.Dir(contPath), filepath.Base(contPath)
	stem, _, ok := strings.Cut(base, ".so")
	if !ok || stem == "" {
		return nil
	}
	target, err := filepath.EvalSymlinks(hostPath)
	if err != nil {
		return nil
	}
	entries, err := os.ReadDir(filepath.Dir(hostPath))
	if err != nil {
		return nil
	}
	var out []deploykit.ResolvedBindMount
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, stem+".so") || name == base {
			continue
		}
		resolved, err := filepath.EvalSymlinks(filepath.Join(filepath.Dir(hostPath), name))
		if err != nil || resolved != target {
			continue
		}
		out = append(out, deploykit.ResolvedBindMount{
			Name: strings.TrimPrefix(name, "lib"),
			// The SOURCE is the host SYMLINK (e.g. /usr/lib/libcuda.so.1), not the versioned file
			// it points at: a driver upgrade replaces the versioned target and removes the old
			// versioned path, so a unit bound to it fails with "no such file or directory" until
			// it is regenerated. The SONAME symlinks survive the upgrade.
			HostPath: filepath.Join(filepath.Dir(hostPath), name), ContPath: filepath.Join(dir, name), ReadOnly: true,
		})
	}
	return out
}

// mountIsReadOnly reports whether the spec already mounts the source read-only.
func mountIsReadOnly(opts []string) bool {
	return slices.Contains(opts, "ro")
}

// writeDevicesOnlyCDI derives the spec into the charly data root and returns its directory. The
// directory is a RUNTIME dependency of the generated unit, not a cache entry, which is why it lives
// under DataDir: `charly clean` must not evict a file the unit's `--cdi-spec-dir` still points at.
func writeDevicesOnlyCDI() (string, []deploykit.ResolvedBindMount, error) {
	spec, libs, err := deriveDevicesOnlyCDI(hostCDISpecPath)
	if err != nil {
		return "", nil, err
	}
	base, err := storeroot.DataDir()
	if err != nil {
		return "", nil, fmt.Errorf("resolving the charly data root: %w", err)
	}
	dir := filepath.Join(base, "cdi")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", nil, fmt.Errorf("creating %s: %w", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "nvidia-devonly.yaml"), spec, 0o644); err != nil {
		return "", nil, fmt.Errorf("writing the derived CDI spec: %w", err)
	}
	return dir, libs, nil
}

// keepIDGPUInputs resolves what a keep-id GPU unit needs — the derived spec directory and the
// library binds — or ("", nil) when the deploy is not in that shape or the host carries no CDI
// spec. The ONE guard and the ONE fallback for both emitters: a unit with no CDI device, no bind
// mount or `--privileged` keeps the host's stock spec, whose mounts apply fine under a mapping that
// is not keep-id, and a failed derivation warns and leaves the unit exactly as it was rather than
// making it unstartable by a flag pointing nowhere.
func keepIDGPUInputs(gpu, privileged bool, bindMounts int) (string, []deploykit.ResolvedBindMount) {
	if !gpu || privileged || bindMounts == 0 {
		return "", nil
	}
	dir, libs, err := writeDevicesOnlyCDI()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: keeping the stock CDI spec: %v\n", err)
		return "", nil
	}
	return dir, libs
}

// applyKeepIDGPU appends the derived spec directory, the library binds and the loader path to a
// quadlet config.
func applyKeepIDGPU(cfg *deploykit.QuadletConfig, privileged bool) {
	dir, libs := keepIDGPUInputs(cfg.GPU, privileged, len(cfg.BindMounts))
	if dir == "" {
		return
	}
	cfg.PodmanArgs = append(cfg.PodmanArgs, "--cdi-spec-dir", dir)
	cfg.BindMounts = append(cfg.BindMounts, libs...)
	cfg.Env = prependLibraryPath(cfg.Env, libDirs(libs))
}

// libDirs returns the unique container directories the derived binds land in, in bind order.
func libDirs(libs []deploykit.ResolvedBindMount) []string {
	seen := map[string]bool{}
	var dirs []string
	for _, l := range libs {
		d := filepath.Dir(l.ContPath)
		if d == "" || d == "." || seen[d] {
			continue
		}
		seen[d] = true
		dirs = append(dirs, d)
	}
	return dirs
}

// prependLibraryPath puts the derived library directories at the FRONT of LD_LIBRARY_PATH, keeping
// any value the image already carries. A devices-only spec drops the toolkit's hooks, so nothing
// refreshes the loader cache at container start: MEASURED, /etc/ld.so.cache still carries its
// image-build timestamp and holds zero libcuda entries, while the library files ARE bound at
// /usr/lib. A consumer that resolves the driver through the cache — triton's libcuda_dirs(), which
// reads `ldconfig -p` and falls back to LD_LIBRARY_PATH — then asserts "libcuda.so cannot found!"
// even though direct dlopen users (torch) still work. The loader honours LD_LIBRARY_PATH, and
// triton accepts it explicitly, so the injected directories belong on it.
func prependLibraryPath(env []string, dirs []string) []string {
	if len(dirs) == 0 {
		return env
	}
	mine := make(map[string]bool, len(dirs))
	for _, d := range dirs {
		mine[d] = true
	}
	// Idempotent: a dir the value already carries is not added twice, so re-applying the merge
	// (the quadlet config then the argv built from it) leaves one copy of each.
	parts := append([]string{}, dirs...)
	for i, e := range env {
		if !strings.HasPrefix(e, "LD_LIBRARY_PATH=") {
			continue
		}
		for _, p := range strings.Split(strings.TrimPrefix(e, "LD_LIBRARY_PATH="), ":") {
			if p != "" && !mine[p] {
				parts = append(parts, p)
			}
		}
		out := make([]string, len(env))
		copy(out, env)
		out[i] = "LD_LIBRARY_PATH=" + strings.Join(parts, ":")
		return out
	}
	return append(env, "LD_LIBRARY_PATH="+strings.Join(parts, ":"))
}

// keepIDGPUEnv is the env twin of keepIDGPUArgs: the loader path the derived binds need.
func keepIDGPUEnv(env []string, gpu, privileged bool, bindMounts int) []string {
	dir, libs := keepIDGPUInputs(gpu, privileged, bindMounts)
	if dir == "" {
		return env
	}
	return prependLibraryPath(env, libDirs(libs))
}

// keepIDGPUArgs is the argv twin of applyKeepIDGPU for the direct/systemd-unit launch and the
// transient `charly shell` container, which carries the same device and the same keep-id mapping.
func keepIDGPUArgs(gpu, privileged bool, bindMounts int) []string {
	dir, libs := keepIDGPUInputs(gpu, privileged, bindMounts)
	if dir == "" {
		return nil
	}
	args := []string{"--cdi-spec-dir", dir}
	for _, l := range libs {
		args = append(args, "-v", l.HostPath+":"+l.ContPath+":ro")
	}
	return args
}
