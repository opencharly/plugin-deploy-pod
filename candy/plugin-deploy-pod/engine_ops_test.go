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
	argv, mode, err := engineRunArgv("containerd", false, podEngineRun{
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
	argv, mode, err := engineRunArgv("podman", false, podEngineRun{
		Image:      "ghcr.io/opencharly/demo:latest",
		Name:       "charly-demo",
		Network:    "charly",
		WorkingDir: "/home/user",
		Hostname:   "charly-demo",
		Detach:     true,
		Remove:     true,
		KeepID:     true,
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
	argv, _, err := engineRunArgv("containerd", false, podEngineRun{
		Image: "ghcr.io/opencharly/demo:latest", Name: "charly-demo",
	})
	if err != nil {
		t.Fatalf("engineRunArgv(containerd): %v", err)
	}
	files, err := engineUnitFiles("containerd", spec.EngineUnitRequest{
		Name: "demo", RunMode: "systemd-unit", StartArgv: argv,
		StopArgv: engineStopArgv("containerd", "charly-demo"),
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
	rt := kitResolvedRuntimeForTest("containerd", "systemd-unit", "sudo")
	if !rootfulUnsupported(rt) {
		t.Fatal("a rootful containerd systemd-unit target must be refused, not silently run rootless")
	}
	if err := rootfulRefusal("containerd", "systemd-unit"); err == nil ||
		!strings.Contains(err.Error(), "rootless") || !strings.Contains(err.Error(), "remedy") {
		t.Errorf("refusal must name the remedy, got %v", err)
	}
	// The podman quadlet control: its rootful mode is not a refusal (the capability declares no
	// privilege prefix and no system-scope unit need here), so the podman plan stays unchanged.
	podman := kitResolvedRuntimeForTest("podman", "quadlet", "sudo")
	if rootfulUnsupported(podman) {
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

	written, err := writeSystemdUnitConfig(rt, qcfg, nil, nil, nil)
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
	if _, err := writeSystemdUnitConfig(rootful, qcfg, nil, nil, nil); err == nil || !strings.Contains(err.Error(), "rootless") {
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
	directDeploy := spec.PodLifecyclePlan{Mode: "quadlet", DirectDeploy: true, ContainerName: "charly-demo"}
	if cmd, _, _, err := podStartCommand(directDeploy); err != nil || cmd != "podman start 'charly-demo'" {
		t.Errorf("direct-deploy start: got %q, %v", cmd, err)
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
	plan, err := resolvePodStartSystemdUnit(context.Background(), nil, "demo", "", rt)
	if err != nil {
		t.Fatalf("resolvePodStartSystemdUnit: %v", err)
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

// TestEngineKeepID_ContainerdHasNoKeepIDArg pins the keep-id fact to the capability row: the
// containerd row leaves UsernsKeepIDArg empty, so nerdctl never gets podman's spelling.
func TestEngineKeepID_ContainerdHasNoKeepIDArg(t *testing.T) {
	if engineKeepID("containerd", 1) {
		t.Error("containerd must not emit a keep-id argument")
	}
	if !engineKeepID("podman", 1) {
		t.Error("podman's capability row declares keep-id; the podman control arm must keep it")
	}
}

// TestWriteSystemdUnitConfig_RejectsBareEnv proves a bare `KEY` env entry is refused with the
// reason, not silently dropped from the unit.
func TestWriteSystemdUnitConfig_RejectsBareEnv(t *testing.T) {
	rt := kitResolvedRuntimeForTest("containerd", "systemd-unit", "auto")
	qcfg := deploykit.QuadletConfig{BoxName: "demo", ImageRef: "img", Env: []string{"FOO"}}
	if _, err := writeSystemdUnitConfig(rt, qcfg, nil, nil, nil); err == nil || !strings.Contains(err.Error(), "inherit host environment variable") {
		t.Errorf("bare env entry must be refused, got %v", err)
	}
}
