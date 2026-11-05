package deploypod

import (
	"encoding/json"
	"testing"

	"github.com/opencharly/sdk"
	"github.com/opencharly/spec/spec"
)

// TestDispatchBuildEnsurePeer_CarriesProjectDir is the regression witness for the
// containerd-bed blocker measured while proving opencharly/charly#1109: a disposable pod
// bed's image is built LOCALLY under the project's registry/name, so when the run engine's
// store lacks it and the registry pull misses, build:ensure must rebuild it FROM THE
// PROJECT. Without `dir` the build fallback fails "buildkitdrive: build: no context", so
// the deploy never reaches config/start — and a pod bed can never run on containerd.
//
// Fails on the pre-fix request (the field was absent; the function had no dir parameter).
func TestDispatchBuildEnsurePeer_CarriesProjectDir(t *testing.T) {
	const (
		image = "ghcr.io/opencharly/probe-engine-image:check-engine-containerd-pod-2026.309.1930"
		dir   = "/home/operator/project"
	)
	client := &recordExecutorClient{}
	if err := dispatchBuildEnsurePeer(t.Context(), sdk.NewInProcExecutor(client), image, "containerd", dir); err != nil {
		t.Fatalf("dispatchBuildEnsurePeer: %v", err)
	}
	if len(client.invoked) != 1 || client.invoked[0] != "build:ensure" {
		t.Fatalf("dispatches = %v, want exactly [build:ensure]", client.invoked)
	}
	if len(client.params) != 1 {
		t.Fatalf("captured params = %d, want 1", len(client.params))
	}
	var req spec.BuildEnsureRequest
	if err := json.Unmarshal(client.params[0], &req); err != nil {
		t.Fatalf("decode the build:ensure request: %v", err)
	}
	if req.Image != image {
		t.Errorf("Image = %q, want %q", req.Image, image)
	}
	if req.RunEngine != "containerd" {
		t.Errorf("RunEngine = %q, want containerd", req.RunEngine)
	}
	if req.Dir != dir {
		t.Fatalf("Dir = %q, want %q — build:ensure cannot rebuild a locally-built bed image into the containerd store without the project context", req.Dir, dir)
	}
}
