package deploypod

import (
	"context"
	"strings"
	"testing"

	"github.com/opencharly/sdk"
	"github.com/opencharly/sdk/deploykit"
	"github.com/opencharly/sdk/kit"
	pb "github.com/opencharly/spec/proto"
	"github.com/opencharly/spec/spec"
	"google.golang.org/grpc"
)

// selection_test.go — the W3.1/W3.7 consumer arms. Each migrated call site is pinned through the
// resolver output it now consumes, and every arm is written so the PRE-CHANGE tree fails it:
// the pre-change lookup had no vocabulary check, the argv builder read the capability's default
// namespace, the rootful predicate ignored the deploy entry, and the presence probe ran
// container.LocalImageExists without a daemon address or namespace.

// selectionForTest resolves a selection the way production does, from a deploy level + runtime.
func selectionForTest(t *testing.T, deploy deploykit.Selection, runtimeEngine string) podSelection {
	t.Helper()
	rt := &kit.ResolvedRuntime{RunEngine: runtimeEngine}
	sel, err := resolvePodSelection(deploy, rt)
	if err != nil {
		t.Fatalf("resolvePodSelection(%+v, %q): %v", deploy, runtimeEngine, err)
	}
	return sel
}

// targetForTest is the resolved target for an engine with no deploy-level overrides.
func targetForTest(t *testing.T, engine string) deploykit.ContainerdTarget {
	t.Helper()
	return selectionForTest(t, deploykit.Selection{}, engine).Target
}

// TestResolvePodSelection_DeployLevelWinsOverRuntime pins the precedence level the old
// hand-rolled deploy-entry-else-runtime lookup implemented — now through the ONE resolver.
func TestResolvePodSelection_DeployLevelWinsOverRuntime(t *testing.T) {
	sel := selectionForTest(t, deploykit.Selection{Engine: "containerd"}, "podman")
	if sel.Engine != "containerd" {
		t.Fatalf("engine = %q, want containerd (the deploy entity outranks the runtime)", sel.Engine)
	}
	if sel.Target.Engine != "containerd" {
		t.Fatalf("target engine = %q, want containerd", sel.Target.Engine)
	}
}

// TestResolvePodSelection_RejectsRetiredEngineWord is the control for the silent-wrong-engine
// class: the pre-change lookup returned the authored word verbatim, and
// container.EngineCapabilityFor — the table kit.EngineBinary reads — resolves ANY unknown word to
// the default engine. A retired `nerdctl` deploy therefore ran podman without an error. The
// resolver rejects it.
func TestResolvePodSelection_RejectsRetiredEngineWord(t *testing.T) {
	rt := &kit.ResolvedRuntime{RunEngine: "podman"}
	if _, err := resolvePodSelection(deploykit.Selection{Engine: "nerdctl"}, rt); err == nil {
		t.Fatal("a retired engine word must be rejected, not silently resolved to the default engine")
	}
	// The control that proves the pre-change path really did fall back silently.
	if got := kit.EngineBinary("nerdctl"); got != spec.DefaultContainerEngine {
		t.Fatalf("control: EngineBinary(nerdctl) = %q, want the silent default %q", got, spec.DefaultContainerEngine)
	}
}

// TestDeployNamespaceReachesEngineArgv pins the resolved namespace into the argv the engine
// actually receives. The pre-change engineGlobalArgs read the capability's default ("default"
// for containerd), so a deploy/settings namespace never reached the CLI.
func TestDeployNamespaceReachesEngineArgv(t *testing.T) {
	deploy := deploykit.Selection{Engine: "containerd", Namespace: "edge"}
	sel := selectionForTest(t, deploy, "podman")
	if sel.Target.Namespace != "edge" {
		t.Fatalf("target namespace = %q, want edge", sel.Target.Namespace)
	}
	argv := strings.Join(engineStopArgv(sel.Target, "charly-demo"), " ")
	if !strings.Contains(argv, "--namespace edge") {
		t.Errorf("stop argv must carry the resolved namespace: %q", argv)
	}
	if strings.Contains(argv, "--namespace default") {
		t.Errorf("stop argv must not carry the capability default when a deploy namespace is set: %q", argv)
	}

	// podman declares no daemon address/namespace: the argv is unchanged by the migration.
	podman := selectionForTest(t, deploykit.Selection{Engine: "podman"}, "podman")
	if got := engineStopArgv(podman.Target, "charly-demo"); strings.Join(got, " ") != "podman stop charly-demo" {
		t.Errorf("podman stop argv changed: %q", got)
	}
}

// TestDeployRootfulReachesTarget pins the deploy-entity rootful level: the pre-change predicate read rt only,
// so `rootful: sudo` on the entry never reached the launch. The resolver folds it into the
// target, which is what rootfulUnsupported and the launch argv read.
func TestDeployRootfulReachesTarget(t *testing.T) {
	podman := selectionForTest(t, deploykit.Selection{Engine: "podman", Rootful: "sudo"}, "podman")
	if !podman.Target.Rootful {
		t.Fatal("a deploy-level rootful mode must reach the resolved target")
	}
	if rootfulUnsupported(podman.Target, "quadlet") {
		t.Error("a rootful podman quadlet target must keep its existing behaviour")
	}

	containerd := selectionForTest(t, deploykit.Selection{Engine: "containerd", Rootful: "sudo"}, "podman")
	if !rootfulUnsupported(containerd.Target, "systemd-unit") {
		t.Error("a rootful containerd systemd-unit target must be refused, not silently run rootless")
	}
}

// TestEngineRunArgv_UsesResolvedTarget carries the target through the launch builder too: the
// containerd plan addresses the resolved namespace and reads its rootful mode from the target.
func TestEngineRunArgv_UsesResolvedTarget(t *testing.T) {
	sel := selectionForTest(t, deploykit.Selection{Engine: "containerd", Namespace: "edge"}, "podman")
	argv, _, err := engineRunArgv(sel.Target, podEngineRun{Image: "img", Name: "charly-demo"})
	if err != nil {
		t.Fatalf("engineRunArgv: %v", err)
	}
	joined := strings.Join(argv, " ")
	if !strings.Contains(joined, "--namespace edge") {
		t.Errorf("launch argv must carry the resolved namespace: %q", joined)
	}
	if argv[0] != spec.EngineBinaries["containerd"] {
		t.Errorf("launch binary = %q, want the containerd capability binary", argv[0])
	}
}

// recordExecutorClient is a capture-only pb.ExecutorServiceClient: RunCapture answers from a
// handler and records every script, so the test sees exactly what the plugin sent to the venue.
type recordExecutorClient struct {
	pb.ExecutorServiceClient
	scripts []string
	handle  func(script string) *pb.CaptureReply
	invoked []string
}

func (c *recordExecutorClient) RunCapture(_ context.Context, in *pb.RunRequest, _ ...grpc.CallOption) (*pb.CaptureReply, error) {
	c.scripts = append(c.scripts, in.GetScript())
	return c.handle(in.GetScript()), nil
}

func (c *recordExecutorClient) InvokeProvider(_ context.Context, in *pb.InvokeProviderRequest, _ ...grpc.CallOption) (*pb.InvokeReply, error) {
	c.invoked = append(c.invoked, in.GetClass()+":"+in.GetReserved())
	return &pb.InvokeReply{}, nil
}

// TestEnsureImagePresent_ProbesResolvedStore pins W3.7's store as the staging boundary: the
// presence probe is the RESOLVED target's argv (address + namespace), not
// container.LocalImageExists run without either. The absent arm then reaches build:ensure and
// re-probes the same store, proving the image landed.
func TestEnsureImagePresent_ProbesResolvedStore(t *testing.T) {
	sel := selectionForTest(t, deploykit.Selection{Engine: "containerd", Namespace: "edge"}, "podman")

	t.Run("present in the resolved store", func(t *testing.T) {
		client := &recordExecutorClient{handle: func(string) *pb.CaptureReply {
			return &pb.CaptureReply{ExitCode: 0}
		}}
		if err := ensureImagePresent(t.Context(), sdk.NewInProcExecutor(client), "myimage:latest", sel.Target); err != nil {
			t.Fatalf("ensureImagePresent: %v", err)
		}
		if len(client.scripts) != 1 {
			t.Fatalf("probe count = %d, want exactly the target-store probe: %q", len(client.scripts), client.scripts)
		}
		probe := client.scripts[0]
		if !strings.Contains(probe, "--namespace 'edge'") || !strings.Contains(probe, "image inspect") {
			t.Errorf("presence probe must address the resolved store: %q", probe)
		}
	})

	t.Run("absent reaches build:ensure and re-probes", func(t *testing.T) {
		probes := 0
		client := &recordExecutorClient{handle: func(script string) *pb.CaptureReply {
			if strings.Contains(script, "image inspect") {
				probes++
				// The pre-build probes (ResolveImage's step 1 and the explicit re-probe
				// that keeps a daemon fault distinct from a miss) answer absent; the
				// post-build re-probe answers present.
				if probes <= 2 {
					return &pb.CaptureReply{ExitCode: 1}
				}
				return &pb.CaptureReply{ExitCode: 0}
			}
			// The engine's own daemon probe (`info`) answers, so exit 1 means absent.
			return &pb.CaptureReply{ExitCode: 0}
		}}
		if err := ensureImagePresent(t.Context(), sdk.NewInProcExecutor(client), "myimage:latest", sel.Target); err != nil {
			t.Fatalf("ensureImagePresent: %v", err)
		}
		if len(client.invoked) != 1 || client.invoked[0] != "build:ensure" {
			t.Fatalf("build:ensure dispatches = %q, want exactly one", client.invoked)
		}
		if probes < 2 {
			t.Fatalf("the store must be re-probed after build:ensure, probes = %d", probes)
		}
		for _, script := range client.scripts {
			if strings.Contains(script, "image inspect") && !strings.Contains(script, "--namespace 'edge'") {
				t.Errorf("every presence probe must address the resolved namespace: %q", script)
			}
		}
	})
}
