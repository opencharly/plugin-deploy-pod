package deploypod

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/opencharly/sdk/deploykit"
	"github.com/opencharly/sdk/kit"
	"github.com/opencharly/spec/container"
	"github.com/opencharly/spec/spec"
)

// engine_ops_test.go — the W3.3 arms. The subject is the pod's engine-op path: a containerd
// pod plan must be a nerdctl argv carrying the capability's runtime handler and daemon
// endpoint, and a podman pod plan must keep the argv it had (no containerd-only flags, the
// same keep-id/userns spelling).

func hasFlagValue(argv []string, flag, value string) bool {
	for i := 0; i+1 < len(argv); i++ {
		if argv[i] == flag && argv[i+1] == value {
			return true
		}
	}
	return false
}

func hasFlag(argv []string, flag string) bool {
	for _, a := range argv {
		if a == flag {
			return true
		}
	}
	return false
}

func TestEngineRunArgv_ContainerdUnitPlan(t *testing.T) {
	argv, mode, err := engineRunArgv(selectionForTest(t, deploykit.Selection{}, "containerd").Target, podEngineRun{
		Image:      "ghcr.io/opencharly/demo:latest",
		Name:       "charly-demo",
		Network:    "charly",
		WorkingDir: "/home/user",
		Hostname:   "charly-demo",
		Ports:      []string{"127.0.0.1:8080:8080"},
	})
	if err != nil {
		t.Fatalf("engineRunArgv(containerd): %v", err)
	}
	if mode != "systemd-unit" {
		t.Errorf("containerd run mode: got %q, want systemd-unit", mode)
	}
	if len(argv) == 0 || argv[0] != "nerdctl" {
		t.Fatalf("containerd plan must launch the engine's binary nerdctl, got %q", argv)
	}
	// The whole point of W3.3: the launch selects a runtime handler from the capability row,
	// never relying on the daemon default.
	capability, ok := engineCapabilityForTest("containerd")
	if !ok || len(capability.RuntimeHandlers) == 0 {
		t.Fatal("containerd capability row has no runtime handlers")
	}
	if !hasFlagValue(argv, "--runtime", capability.RuntimeHandlers[0]) {
		t.Errorf("containerd plan missing --runtime %q: %q", capability.RuntimeHandlers[0], argv)
	}
	// The daemon endpoint and namespace ride before the subcommand, where the CLI accepts them.
	if !hasFlagValue(argv, "--address", capability.Address) {
		t.Errorf("containerd plan missing --address %q: %q", capability.Address, argv)
	}
	if !hasFlagValue(argv, "--namespace", capability.Namespace) {
		t.Errorf("containerd plan missing --namespace %q: %q", capability.Namespace, argv)
	}
	if strings.Join(argv, " ") == "" || argv[1] != "--address" {
		t.Errorf("containerd global flags must precede the subcommand: %q", argv)
	}
	if hasFlag(argv, "-d") {
		t.Errorf("the systemd-unit ExecStart must be foreground, got %q", argv)
	}
	t.Logf("containerd pod plan: %q", argv)
}

func TestEngineRunArgv_PodmanPlanUnchanged(t *testing.T) {
	argv, mode, err := engineRunArgv(selectionForTest(t, deploykit.Selection{}, "podman").Target, podEngineRun{
		Image:      "ghcr.io/opencharly/demo:latest",
		Name:       "charly-demo",
		Network:    "charly",
		WorkingDir: "/home/user",
		Hostname:   "charly-demo",
		Detach:     true,
		Remove:     true,
		UserNS:     userNSKeepID,
		UID:        1000,
		GID:        1000,
		Ports:      []string{"127.0.0.1:8080:8080"},
		Volumes:    []string{"charly-demo-state:/home/user"},
	})
	if err != nil {
		t.Fatalf("engineRunArgv(podman): %v", err)
	}
	if mode != "quadlet" {
		t.Errorf("podman run mode: got %q, want quadlet", mode)
	}
	if argv[0] != "podman" {
		t.Errorf("podman plan binary: got %q", argv[0])
	}
	for _, want := range [][2]string{
		{"-d", ""}, {"--rm", ""}, {"--name", "charly-demo"},
		{"--hostname", "charly-demo"}, {"-w", "/home/user"},
		{"--network", "charly"}, {"-p", "127.0.0.1:8080:8080"},
		{"-v", "charly-demo-state:/home/user"},
	} {
		if want[1] == "" {
			if !hasFlag(argv, want[0]) {
				t.Errorf("podman plan lost %q: %q", want[0], argv)
			}
			continue
		}
		if !hasFlagValue(argv, want[0], want[1]) {
			t.Errorf("podman plan lost %s %s: %q", want[0], want[1], argv)
		}
	}
	if !hasFlag(argv, "--userns=keep-id:uid=1000,gid=1000") {
		t.Errorf("podman plan lost its keep-id uid/gid mapping: %q", argv)
	}
	// The control: no containerd-only flag may appear on a podman plan.
	for _, forbidden := range []string{"--runtime", "--address", "--namespace"} {
		if hasFlag(argv, forbidden) {
			t.Errorf("podman plan must not carry %q: %q", forbidden, argv)
		}
	}
	t.Logf("podman pod plan: %q", argv)
}

func TestEngineUnitFiles_ContainerdUnitWrapsStartPlan(t *testing.T) {
	sel := selectionForTest(t, deploykit.Selection{}, "containerd")
	argv, _, err := engineRunArgv(sel.Target, podEngineRun{
		Image: "ghcr.io/opencharly/demo:latest", Name: "charly-demo",
	})
	if err != nil {
		t.Fatalf("engineRunArgv(containerd): %v", err)
	}
	files, err := engineUnitFiles("containerd", spec.EngineUnitRequest{
		Name: "demo", RunMode: "systemd-unit", StartArgv: argv,
		StopArgv: engineStopArgv(sel.Target, "charly-demo"),
	})
	if err != nil {
		t.Fatalf("engineUnitFiles(containerd): %v", err)
	}
	body, ok := files["charly-demo.service"]
	if !ok {
		t.Fatalf("unit_emit did not return charly-demo.service: %v", files)
	}
	if !strings.Contains(body, "ExecStart=nerdctl ") || !strings.Contains(body, "--runtime ") {
		t.Errorf("unit ExecStart does not wrap the engine start_plan argv:\n%s", body)
	}
	if !strings.Contains(body, "ExecStop=nerdctl ") {
		t.Errorf("unit does not carry the engine stop argv:\n%s", body)
	}
}

func TestRootfulContainerdRefused(t *testing.T) {
	containerd := selectionForTest(t, deploykit.Selection{Rootful: "sudo"}, "containerd")
	if !rootfulUnsupported(containerd.Target, "systemd-unit") {
		t.Fatal("a rootful containerd systemd-unit target must be refused, not silently run rootless")
	}
	if err := rootfulRefusal("containerd", "systemd-unit"); err == nil ||
		!strings.Contains(err.Error(), "rootless") || !strings.Contains(err.Error(), "remedy") {
		t.Errorf("refusal must name the remedy, got %v", err)
	}
	// The podman quadlet control: its rootful mode is not a refusal (the capability declares no
	// privilege prefix and no system-scope unit need here), so the podman plan stays unchanged.
	podman := selectionForTest(t, deploykit.Selection{Rootful: "sudo"}, "podman")
	if rootfulUnsupported(podman.Target, "quadlet") {
		t.Error("podman quadlet rootful must keep its existing behaviour")
	}
}

// kitResolvedRuntimeForTest builds the runtime shape the refusal reads, without touching the
// operator's config file.
func kitResolvedRuntimeForTest(engine, mode, rootful string) *kit.ResolvedRuntime {
	return &kit.ResolvedRuntime{RunEngine: engine, RunMode: mode, Rootful: rootful}
}

// TestWriteSystemdUnitConfig_Containerd proves the CONFIG path: a containerd systemd-unit
// target writes one supervision file whose ExecStart is the nerdctl start_plan argv, carrying
// the runtime handler from the capability row.
func TestWriteSystemdUnitConfig_Containerd(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	rt := kitResolvedRuntimeForTest("containerd", "systemd-unit", "auto")
	qcfg := deploykit.QuadletConfig{BoxName: "demo", ImageRef: "img", Home: "/home/user"}

	written, err := writeSystemdUnitConfig(rt, selectionForTest(t, deploykit.Selection{}, "containerd"), qcfg, nil, nil, nil)
	if err != nil {
		t.Fatalf("writeSystemdUnitConfig: %v", err)
	}
	wantPath := filepath.Join(home, ".config", "systemd", "user", "charly-demo.service")
	if len(written) != 1 || written[0] != wantPath {
		t.Fatalf("written paths: got %v, want [%s]", written, wantPath)
	}
	body, err := os.ReadFile(wantPath)
	if err != nil {
		t.Fatalf("read unit: %v", err)
	}
	text := string(body)
	t.Logf("containerd systemd unit:\n%s", text)
	capability, _ := container.EngineCapabilityFor("containerd")
	for _, want := range []string{"ExecStart=nerdctl ", "--runtime " + capability.RuntimeHandlers[0],
		"--address " + capability.Address, "--namespace " + capability.Namespace} {
		if !strings.Contains(text, want) {
			t.Errorf("unit missing %q:\n%s", want, text)
		}
	}

	// A rootful systemd-unit target is refused before any file is written.
	rootful := kitResolvedRuntimeForTest("containerd", "systemd-unit", "sudo")
	if _, err := writeSystemdUnitConfig(rootful, selectionForTest(t, deploykit.Selection{Rootful: "sudo"}, "containerd"), qcfg, nil, nil, nil); err == nil || !strings.Contains(err.Error(), "rootless") {
		t.Errorf("rootful systemd-unit must be refused with the remedy, got %v", err)
	}
}

// engineCapabilityForTest resolves the capability row through the same spec table the subject
// uses, keeping the test's expectation data-driven rather than retyped.
func engineCapabilityForTest(engine string) (spec.EngineCapability, bool) {
	return container.EngineCapabilityFor(engine)
}

// TestPodStartCommand_UnitRunModesUseSystemctl pins the start decision: BOTH unit modes start
// through systemctl, a direct-deploy marker keeps `podman start`, a direct plan execs its
// argv, and an unknown mode is refused instead of exec'ing an empty argv.
func TestPodStartCommand_UnitRunModesUseSystemctl(t *testing.T) {
	for _, mode := range []string{"quadlet", "systemd-unit"} {
		plan := spec.PodLifecyclePlan{Mode: mode, SvcName: "charly-demo.service", ContainerName: "charly-demo"}
		cmd, label, target, err := podStartCommand(plan)
		if err != nil {
			t.Fatalf("%s: %v", mode, err)
		}
		if want := "systemctl --user start 'charly-demo.service'"; cmd != want {
			t.Errorf("%s start: got %q, want %q", mode, cmd, want)
		}
		if label != "start" || target != "charly-demo.service" {
			t.Errorf("%s start label/target: got %q/%q", mode, label, target)
		}
	}

	direct := spec.PodLifecyclePlan{Mode: "direct", ContainerName: "charly-demo", RunArgv: []string{"nerdctl", "run", "img"}}
	if cmd, _, _, err := podStartCommand(direct); err != nil || cmd != "'nerdctl' 'run' 'img'" {
		t.Errorf("direct start: got %q, %v", cmd, err)
	}
	// A direct-deploy marker starts through the plan's RESOLVED engine binary, never a
	// hard-coded podman (the #37 residue: a docker/containerd direct deploy was started with
	// the wrong binary, or no working one at all).
	for _, engine := range []string{"podman", "docker"} {
		directDeploy := spec.PodLifecyclePlan{Mode: "quadlet", DirectDeploy: true, ContainerName: "charly-demo", EngineBin: engine}
		want := engine + " start 'charly-demo'"
		if cmd, _, _, err := podStartCommand(directDeploy); err != nil || cmd != want {
			t.Errorf("direct-deploy start via %s: got %q, %v; want %q", engine, cmd, err, want)
		}
	}
	// A plan with no resolved engine binary must fail loudly instead of exec'ing an empty argv[0].
	if _, _, _, err := podStartCommand(spec.PodLifecyclePlan{Mode: "quadlet", DirectDeploy: true, ContainerName: "charly-demo"}); err == nil {
		t.Error("direct-deploy start without EngineBin must be refused")
	}
	if _, _, _, err := podStartCommand(spec.PodLifecyclePlan{Mode: "bogus"}); err == nil {
		t.Error("unknown run mode must be refused")
	}
}

// TestResolvePodStartSystemdUnit_StartsViaSystemctl resolves a containerd systemd-unit start
// plan through the real resolver (a unit file present for the deploy) and proves it is started
// by systemctl — never by exec'ing the (empty) launch argv.
func TestResolvePodStartSystemdUnit_StartsViaSystemctl(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	svcDir := filepath.Join(home, ".config", "systemd", "user")
	if err := os.MkdirAll(svcDir, 0o755); err != nil {
		t.Fatalf("mkdir unit dir: %v", err)
	}
	svc := "charly-demo.service"
	if err := os.WriteFile(filepath.Join(svcDir, svc), []byte("[Unit]\nDescription=demo\n"), 0o600); err != nil {
		t.Fatalf("write unit: %v", err)
	}

	rt := kitResolvedRuntimeForTest("containerd", "systemd-unit", "auto")
	plan, sel, err := resolvePodStartSystemdUnit(context.Background(), nil, "demo", "", rt)
	if err != nil {
		t.Fatalf("resolvePodStartSystemdUnit: %v", err)
	}
	if sel.Engine != "containerd" {
		t.Fatalf("resolved selection: got engine %q, want containerd", sel.Engine)
	}
	if plan.Mode != "systemd-unit" || plan.DirectDeploy {
		t.Fatalf("plan mode: got %q directDeploy=%v, want systemd-unit/false", plan.Mode, plan.DirectDeploy)
	}
	if len(plan.RunArgv) != 0 {
		t.Fatalf("systemd-unit plan must not carry a launch argv, got %q", plan.RunArgv)
	}
	cmd, _, target, err := podStartCommand(*plan)
	if err != nil {
		t.Fatalf("podStartCommand: %v", err)
	}
	if want := "systemctl --user start '" + svc + "'"; cmd != want || target != svc {
		t.Errorf("start: got %q/%q, want %q/%q", cmd, target, want, svc)
	}
}

// TestPodUserNS_KeepIDIsTheCapabilityFact pins keep-id to the capability row: the containerd row
// leaves UsernsKeepIDArg empty, so nerdctl never gets podman's spelling, and a bind-only podman
// deploy keeps it exactly as it had it.
func TestPodUserNS_KeepIDIsTheCapabilityFact(t *testing.T) {
	if got := podUserNSFor("containerd", false, false, 1); got.keepID() {
		t.Errorf("containerd must not emit a keep-id argument, got mapping %v", got)
	}
	if got := podUserNSFor("podman", false, false, 1); !got.keepID() {
		t.Errorf("podman's capability row declares keep-id; the bind-only podman control arm must keep it, got mapping %v", got)
	}
	if got := podUserNSFor("podman", false, false, 0); got != userNSImageOwn {
		t.Errorf("no bind mount means keep-id was never in play: got mapping %v", got)
	}
	// A PRIVILEGED gpu deploy carries no CDI device (`--privileged` IS the device grant, and the
	// quadlet renderer has always skipped `AddDevice=` for it), so it keeps keep-id — on every
	// emitter, which is what the cross-emitter guard in volume_root_test.go asserts.
	if got := podUserNSFor("podman", true, true, 1); !got.keepID() {
		t.Errorf("a privileged GPU deploy carries no CDI device and must keep keep-id, got mapping %v", got)
	}
}

// TestPodUserNS_CDIDeviceNeverKeepID is the fix for opencharly/plugin-deploy-pod#81: a deploy that
// carries a CDI GPU device must not run under keep-id, because crun cannot apply the device's FILE
// mounts inside a keep-id user namespace and the container dies at the first one (mount_setattr
// EPERM, exit 126). It runs userns=host with the workload as container root instead — the only
// mapping under which a host-owned bind stays writable AND the device resolves.
//
// The assertion is on the argv the emitters really produce, not only on the predicate, so a
// call site that forgets to consult it fails here: both argv paths are checked, and the keep-id
// spelling must be ABSENT from every one of them.
func TestPodUserNS_CDIDeviceNeverKeepID(t *testing.T) {
	const binds = 1
	bindMounts := []deploykit.ResolvedBindMount{{Name: "data", HostPath: "/srv/data", ContPath: "/data"}}
	sel := selectionForTest(t, deploykit.Selection{}, "podman")

	// The predicate itself: the resolved deploy carries a device, so keep-id is out and the
	// host/root mapping is in.
	if got := podUserNSFor("podman", true, false, binds); got != userNSHostRoot {
		t.Fatalf("a CDI deploy with a bind mount must run userns=host as container root, got mapping %v", got)
	}

	// `charly start` / direct-mode launch (`charly config` in direct mode).
	startArgv, err := buildStartArgs(sel, "img", 1000, 1000, nil, "ctr", nil, bindMounts,
		true, "", nil, spec.SecurityConfig{}, nil, "/")
	if err != nil {
		t.Fatalf("buildStartArgs: %v", err)
	}
	assertHostRootArgv(t, "buildStartArgs", startArgv)

	// `charly shell` — the transient container that attaches the same bind.
	shellArgv := buildShellArgs(sel.Target, "img", 1000, 1000, nil, nil, bindMounts, true,
		"", "", nil, spec.SecurityConfig{}, "/", true)
	assertHostRootArgv(t, "buildShellArgs", shellArgv)

	// `charly exec` — the attach into the running container, which the deploy created as root.
	execArgv := buildExecArgs(sel.Target, "charly-ctr", 1000, 1000, userNSHostRoot, "", nil, "/", true)
	if !hasFlagValue(execArgv, "--user", containerRootUser) {
		t.Errorf("buildExecArgs must attach as container root for a CDI deploy: %q", execArgv)
	}

	// And the control: the SAME bind mounts without a GPU device keep keep-id.
	controlArgv, err := buildStartArgs(sel, "img", 1000, 1000, nil, "ctr", nil, bindMounts,
		false, "", nil, spec.SecurityConfig{}, nil, "/")
	if err != nil {
		t.Fatalf("buildStartArgs(control): %v", err)
	}
	if !hasFlag(controlArgv, "--userns=keep-id:uid=1000,gid=1000") {
		t.Errorf("a bind-only deploy must keep its keep-id mapping unchanged: %q", controlArgv)
	}

	// A PRIVILEGED gpu deploy is the other control, and it must be consistent on BOTH sides of the
	// emitter pair: no explicit device in the argv (the unit carries none either), and therefore
	// the keep-id mapping the unit also emits. Before this arm existed the argv carried
	// `--device nvidia.com/gpu=all` under keep-id — the exact combination that cannot start.
	privSecurity := spec.SecurityConfig{Privileged: true}
	privArgv, err := buildStartArgs(sel, "img", 1000, 1000, nil, "ctr", nil, bindMounts,
		true, "", nil, privSecurity, nil, "/")
	if err != nil {
		t.Fatalf("buildStartArgs(privileged): %v", err)
	}
	if !hasFlag(privArgv, "--userns=keep-id:uid=1000,gid=1000") {
		t.Errorf("a privileged GPU deploy keeps keep-id in the argv, like its unit: %q", privArgv)
	}
	if hasFlag(privArgv, "--device") {
		t.Errorf("a privileged launch must not add a CDI device the unit does not carry: %q", privArgv)
	}
	privShell := buildShellArgs(sel.Target, "img", 1000, 1000, nil, nil, bindMounts, true,
		"", "", nil, privSecurity, "/", true)
	if !hasFlag(privShell, "--userns=keep-id:uid=1000,gid=1000") || hasFlag(privShell, "--device") {
		t.Errorf("charly shell must agree with charly start on a privileged deploy: %q", privShell)
	}
	if got := podGPUArgs("podman", true, true); got != nil {
		t.Errorf("a privileged deploy gets no explicit GPU args, got %q", got)
	}
	if got := podGPUArgs("podman", true, false); len(got) == 0 {
		t.Error("an unprivileged CDI deploy must still carry its device arguments")
	}
}

// assertHostRootArgv asserts the CDI mapping in an argv: userns=host present, the workload as
// container root, and NO keep-id spelling anywhere.
func assertHostRootArgv(t *testing.T, who string, argv []string) {
	t.Helper()
	if !hasFlagValue(argv, "--userns", "host") && !hasFlag(argv, "--userns=host") {
		t.Errorf("%s: expected userns=host, got %q", who, argv)
	}
	if !hasFlagValue(argv, "--user", containerRootUser) {
		t.Errorf("%s: expected the workload as container root (--user %s), got %q", who, containerRootUser, argv)
	}
	for _, a := range argv {
		if strings.HasPrefix(a, "--userns=keep-id") {
			t.Errorf("%s: a CDI deploy must not carry a keep-id mapping, got %q", who, argv)
		}
	}
}

// countFlagValue returns how many times `flag value` appears as an adjacent pair in argv.
func countFlagValue(argv []string, flag, value string) int {
	n := 0
	for i := 0; i+1 < len(argv); i++ {
		if argv[i] == flag && argv[i+1] == value {
			n++
		}
	}
	return n
}

// TestEngineRunArgv_EnvEntriesVerbatim is the table-driven guard for the env shape: the engine
// CLI accepts BOTH a bare `KEY` entry (inherit the variable from the launching process) and a
// `KEY=VALUE` entry, and both must reach the argv byte-for-byte. #38: the former map
// conversion kept only entries containing `=`, so `-e FOO` vanished silently and the
// container started without the variable.
func TestEngineRunArgv_EnvEntriesVerbatim(t *testing.T) {
	cases := []struct {
		name string
		env  []string
	}{
		{"bare inherit", []string{"FOO"}},
		{"pair", []string{"BAR=1"}},
		{"empty value", []string{"EMPTY="}},
		{"value contains equals", []string{"EQ=a=b"}},
		{"all forms together", []string{"FOO", "BAR=1", "EMPTY=", "EQ=a=b"}},
	}
	for _, engine := range []string{"podman", "containerd"} {
		for _, tc := range cases {
			t.Run(engine+"/"+tc.name, func(t *testing.T) {
				argv, _, err := engineRunArgv(selectionForTest(t, deploykit.Selection{}, engine).Target, podEngineRun{
					Image: "ghcr.io/opencharly/demo:latest",
					Name:  "charly-demo",
					Env:   tc.env,
				})
				if err != nil {
					t.Fatalf("engineRunArgv(%s): %v", engine, err)
				}
				for _, entry := range tc.env {
					if got := countFlagValue(argv, "-e", entry); got != 1 {
						t.Errorf("env entry %q reached the engine %d times, want exactly 1: %q", entry, got, argv)
					}
				}
				// No entry may be split, re-keyed or invented: one `-e` per input entry.
				total := 0
				for i := 0; i+1 < len(argv); i++ {
					if argv[i] == "-e" {
						total++
					}
				}
				if total != len(tc.env) {
					t.Errorf("got %d -e flags for %d env entries: %q", total, len(tc.env), argv)
				}
			})
		}
	}
}

// TestWriteSystemdUnitConfig_RejectsBareEnv proves a bare `KEY` env entry is refused with the
// reason, not silently dropped from the unit.
func TestWriteSystemdUnitConfig_RejectsBareEnv(t *testing.T) {
	rt := kitResolvedRuntimeForTest("containerd", "systemd-unit", "auto")
	qcfg := deploykit.QuadletConfig{BoxName: "demo", ImageRef: "img", Env: []string{"FOO"}}
	if _, err := writeSystemdUnitConfig(rt, selectionForTest(t, deploykit.Selection{}, "containerd"), qcfg, nil, nil, nil); err == nil || !strings.Contains(err.Error(), "inherit host environment variable") {
		t.Errorf("bare env entry must be refused, got %v", err)
	}
}
