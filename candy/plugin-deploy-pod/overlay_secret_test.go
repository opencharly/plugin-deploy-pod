package deploypod

// overlay_secret_test.go — the overlay path's first behavioural test, and the one that pins the
// P07 unit 3 change: a `use: secret:` entry is delivered as a FILE step the host compiler already
// placed in `reply.Plans`, and NOTHING writes the secret's bytes into an OpStep's env.
//
// Until this file, no test and no bed drove `buildOverlay` (opencharly/plugin-deploy-pod#68), so the
// plugin's whole job — rendering the overlay Containerfile — was unproven. The function reaches the
// host per step through `exec.HostBuild("step-emit", …)`; the fixture answers that with a canned
// fragment, which is the ONE substitution a test may make here: the assertion is about the PLAN,
// not about the Containerfile bytes.
//
// Both directions are load-bearing:
//   - re-adding `deploykit.InjectSecretsIntoPlans` puts the value into an OpStep's env and fails
//     the "no env carries the secret" assertion (it renders `export KEY=<value>` into the overlay);
//   - stripping the FileStep from the plans fails the "the step survives" assertion.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/grpc"
	"gopkg.in/yaml.v3"

	"github.com/opencharly/sdk"
	"github.com/opencharly/sdk/deploykit"
	pb "github.com/opencharly/spec/proto"
	"github.com/opencharly/spec/spec"
)

// overlayStub answers the two host RPCs the overlay renderer makes: a per-step fragment and the
// init/egress provider calls. Everything else is left on the embedded nil interface, so an
// unexpected call panics loudly instead of silently returning a zero reply.
type overlayStub struct {
	pb.ExecutorServiceClient
	fragments     int
	venueCommands []string
}

func (s *overlayStub) HostBuild(_ context.Context, in *pb.HostBuildRequest, _ ...grpc.CallOption) (*pb.HostBuildReply, error) {
	s.fragments++
	return &pb.HostBuildReply{ResultJson: []byte(`{"fragment":"RUN true\n"}`)}, nil
}

// RunCapture answers the ONE venue shell command buildOverlay issues at its tail — the overlay
// `build -f … -t …` itself. The assertion in this file is about the PLAN the overlay consumed and
// handed on, never about the image, so the command succeeds without a container engine; this is a
// one-line substitution, not a fake podman.
func (s *overlayStub) RunCapture(_ context.Context, in *pb.RunRequest, _ ...grpc.CallOption) (*pb.CaptureReply, error) {
	s.venueCommands = append(s.venueCommands, in.GetScript())
	return &pb.CaptureReply{}, nil
}

func (s *overlayStub) InvokeProvider(_ context.Context, _ *pb.InvokeProviderRequest, _ ...grpc.CallOption) (*pb.InvokeReply, error) {
	return &pb.InvokeReply{ResultJson: []byte(`{}`)}, nil
}

// TestBuildOverlayKeepsTheCompiledFileStepAndLeaksNothingElse drives buildOverlay with a fixture
// envelope whose candy carries a `use: secret:` entry.
func TestBuildOverlayKeepsTheCompiledFileStepAndLeaksNothingElse(t *testing.T) {
	const secretBytes = "PRIVATE-KEY-MATERIAL-0123456789"
	const dst = "/run/lab/id_ed25519"
	dir := t.TempDir()
	src := filepath.Join(dir, "id_ed25519")
	if err := os.WriteFile(src, []byte(secretBytes), 0o600); err != nil {
		t.Fatalf("writing the host secret: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, ".build"), 0o755); err != nil {
		t.Fatalf("creating .build: %v", err)
	}

	// The plan the HOST compiler produces for this candy — the same call the host makes, so the two
	// halves (compiler, overlay) meet on one fixture. The candy ALSO carries the OLD env-shaped
	// `secret_require:` declaration, because that is what the removed injection consumed: without
	// it the "no env carries the secret" assertion would have nothing to fail on, i.e. it would be
	// vacuous. The value is delivered as a FILE (`use: secret:`) and must not reach env at all.
	candyModel := spec.CandyModel{Name: "lab"}
	candyModel.Use = mustCandyUse(t, src, dst)
	candyModel.SecretRequire = []spec.EnvDependency{{Name: "LAB_KEY"}}
	stub := &overlayStub{}
	ex := sdk.NewInProcExecutor(stub)
	img := deploykit.NewSpecResolvedBox(spec.ResolvedBoxView{Name: "base", Candy: []string{"lab"}}, nil, nil)
	plan, err := deploykit.BuildDeployPlan(context.Background(), ex, deploykit.NewSpecCandyModel(candyModel, spec.CandyView{Name: "lab"}), img, deploykit.HostContext{})
	if err != nil {
		t.Fatalf("BuildDeployPlan: %v", err)
	}
	if len(fileStepsOf(plan.Steps)) != 1 {
		t.Fatalf("the compiler did not put a FileStep in the plan: %#v", plan.Steps)
	}
	// The host compiler also emits OpSteps for a candy's own run: steps; the fixture adds one
	// directly, because it is the env ENTRY POINT the removed injection wrote into.
	plan.Steps = append(plan.Steps, &spec.OpStep{
		CandyName: "lab",
		Op:        &spec.Op{Plugin: "command", PluginInput: map[string]any{"command": "true"}},
	})

	rp := &spec.ResolvedProject{
		Boxes:       map[string]spec.ResolvedBoxView{"base": {Name: "base", Candy: []string{"lab"}}},
		Candies:     map[string]spec.CandyView{"lab": {Name: "lab"}},
		CandyModels: map[string]spec.CandyModel{"lab": candyModel},
	}
	reply := spec.OverlayBuildReply{DeployName: "bed", BaseImage: "base"}
	plans := []*spec.InstallPlan{plan}

	if _, err := buildOverlay(context.Background(), ex, reply, rp, plans, dir, "base", spec.LifecycleOpts{}, "user", nil, "registry.example"); err != nil {
		t.Fatalf("buildOverlay: %v", err)
	}

	// (2) the compiled delivery step survives the overlay.
	if got := fileStepsOf(plans[0].Steps); len(got) != 1 || got[0].Dest != dst {
		t.Fatalf("the overlay lost the compiled FileStep: %#v", got)
	}
	// (3) D-SEC-1: no env in any step carries the value.
	for _, step := range plans[0].Steps {
		op, ok := step.(*spec.OpStep)
		if !ok || op.Op == nil {
			continue
		}
		for k, v := range op.Op.Env {
			if strings.Contains(v, secretBytes) {
				t.Fatalf("OpStep %q env[%q] carries the secret's bytes — D-SEC-1: a value reaches its consumer as a file, never as text", op.Op.Plugin, k)
			}
			// The KEY is the store-independent half: the removed injection set env[<secret_require
			// name>] whether or not the store resolved a value (a required entry auto-generates a
			// token), so its presence is exactly what proves the injection ran.
			if k == "LAB_KEY" {
				t.Fatalf("OpStep %q env carries the secret_require name %q — the env injection is back", op.Op.Plugin, k)
			}
		}
	}
}

// mustCandyUse decodes the authored `use:` block into the generated model — the same path a
// scanner takes, so the fixture cannot drift from the schema.
func mustCandyUse(t *testing.T, src, dst string) *spec.CandyUse {
	t.Helper()
	var m spec.CandyModel
	body := "use:\n  secret:\n    lab-signing-key:\n      src: " + src + "\n      dst: " + dst + "\n"
	if err := yaml.Unmarshal([]byte(body), &m); err != nil {
		t.Fatalf("decoding the use: block: %v", err)
	}
	return m.Use
}

func fileStepsOf(steps []spec.InstallStep) []*deploykit.FileStep {
	var out []*deploykit.FileStep
	for _, s := range steps {
		if fs, ok := s.(*deploykit.FileStep); ok {
			out = append(out, fs)
		}
	}
	return out
}
