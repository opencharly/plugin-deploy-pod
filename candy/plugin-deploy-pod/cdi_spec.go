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
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/opencharly/sdk/deploykit"
	"github.com/opencharly/spec/storeroot"
)

// hostCDISpecPath is the toolkit's default NVIDIA spec. A host with a different one can still be
// served: the derivation reads whatever `nvidia-ctk` wrote, and an absent file simply means there
// is nothing to derive (the caller then leaves the unit as it was).
const hostCDISpecPath = "/etc/cdi/nvidia.yaml"

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
	Path        string `yaml:"path"`
	Major       int64  `yaml:"major,omitempty"`
	Minor       int64  `yaml:"minor,omitempty"`
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
// bound as `<versioned host file> -> <its SONAME path in the container>`.
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
			Name:     strings.TrimPrefix(name, "lib"),
			HostPath: hostPath, ContPath: filepath.Join(dir, name), ReadOnly: true,
		})
	}
	return out
}

// mountIsReadOnly reports whether the spec already mounts the source read-only.
func mountIsReadOnly(opts []string) bool {
	for _, o := range opts {
		if o == "ro" {
			return true
		}
	}
	return false
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

// applyKeepIDGPU appends what a keep-id GPU unit needs to a quadlet config: the engine flag that
// points podman at the derived devices-only spec, and the driver libraries as read-only binds.
//
// It is a no-op for a deploy that is not in the keep-id shape (no CDI device, no bind mount, or
// privileged): those units keep the host's stock spec, whose mounts apply fine under a mapping
// that is not keep-id. A host without a CDI spec is a no-op too — the unit is left exactly as it
// was rather than made unstartable by a flag pointing nowhere.
func applyKeepIDGPU(cfg *deploykit.QuadletConfig, privileged bool) error {
	if !cfg.GPU || privileged || len(cfg.BindMounts) == 0 {
		return nil
	}
	dir, libs, err := writeDevicesOnlyCDI()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: GPU libraries stay on the stock CDI spec: %v\n", err)
		return nil
	}
	cfg.PodmanArgs = append(cfg.PodmanArgs, "--cdi-spec-dir", dir)
	cfg.BindMounts = append(cfg.BindMounts, libs...)
	return nil
}

// keepIDGPUArgs is the argv twin of applyKeepIDGPU for the direct/systemd-unit launch: the same
// `--cdi-spec-dir` and the same read-only library binds, expressed as engine arguments.
func keepIDGPUArgs(gpu, privileged bool, bindMounts int) []string {
	if !gpu || privileged || bindMounts == 0 {
		return nil
	}
	dir, libs, err := writeDevicesOnlyCDI()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: GPU libraries stay on the stock CDI spec: %v\n", err)
		return nil
	}
	args := []string{"--cdi-spec-dir", dir}
	for _, l := range libs {
		args = append(args, "-v", l.HostPath+":"+l.ContPath+":ro")
	}
	return args
}
