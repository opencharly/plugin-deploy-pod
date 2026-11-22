package deploypod

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"google.golang.org/grpc"

	"github.com/opencharly/sdk"
	"github.com/opencharly/sdk/buildkit"
	"github.com/opencharly/sdk/deploykit"
	pb "github.com/opencharly/spec/proto"
	"github.com/opencharly/spec/spec"
)

// ensureStub answers build:ensure and records, for each call, whether the overlay build dir already
// existed — the measured ordering defect is an ensure that prunes the dir created before it.
type ensureStub struct {
	deploy  string
	refs    []string
	dirSeen []bool
	err     error
}

func (s *ensureStub) InvokeProvider(_ context.Context, in *pb.InvokeProviderRequest, _ ...grpc.CallOption) (*pb.InvokeReply, error) {
	if in.GetClass() != "build" || in.GetReserved() != "ensure" {
		return nil, fmt.Errorf("ensureStub: unexpected dispatch %q/%q", in.GetClass(), in.GetReserved())
	}
	var req spec.BuildEnsureRequest
	if err := json.Unmarshal(in.GetParamsJson(), &req); err != nil {
		return nil, err
	}
	// Record whether the OVERLAY DIR (the one prepareOverlayBuild creates) exists right now.
	_, dirErr := os.Stat(filepath.Join(".build", "overlay-"+s.deploy))
	s.dirSeen = append(s.dirSeen, dirErr == nil)
	s.refs = append(s.refs, req.Image)
	if s.err != nil {
		return nil, s.err
	}
	return &pb.InvokeReply{}, nil
}

func (s *ensureStub) Venue(context.Context, *pb.Empty, ...grpc.CallOption) (*pb.VenueReply, error) {
	return nil, fmt.Errorf("ensureStub: Venue not implemented")
}
func (s *ensureStub) RunSystem(context.Context, *pb.RunRequest, ...grpc.CallOption) (*pb.RunReply, error) {
	return nil, fmt.Errorf("ensureStub: RunSystem not implemented")
}
func (s *ensureStub) RunUser(context.Context, *pb.RunRequest, ...grpc.CallOption) (*pb.RunReply, error) {
	return nil, fmt.Errorf("ensureStub: RunUser not implemented")
}
func (s *ensureStub) PutFile(context.Context, *pb.PutFileRequest, ...grpc.CallOption) (*pb.PutFileReply, error) {
	return nil, fmt.Errorf("ensureStub: PutFile not implemented")
}
func (s *ensureStub) RunCapture(context.Context, *pb.RunRequest, ...grpc.CallOption) (*pb.CaptureReply, error) {
	return nil, fmt.Errorf("ensureStub: RunCapture not implemented")
}
func (s *ensureStub) RunInteractive(context.Context, *pb.RunRequest, ...grpc.CallOption) (*pb.LiveReply, error) {
	return nil, fmt.Errorf("ensureStub: RunInteractive not implemented")
}
func (s *ensureStub) RunStream(context.Context, *pb.RunRequest, ...grpc.CallOption) (*pb.LiveReply, error) {
	return nil, fmt.Errorf("ensureStub: RunStream not implemented")
}
func (s *ensureStub) GetFile(context.Context, *pb.GetFileRequest, ...grpc.CallOption) (*pb.GetFileReply, error) {
	return nil, fmt.Errorf("ensureStub: GetFile not implemented")
}
func (s *ensureStub) RunHostStep(context.Context, *pb.HostStepRequest, ...grpc.CallOption) (*pb.HostStepReply, error) {
	return nil, fmt.Errorf("ensureStub: RunHostStep not implemented")
}
func (s *ensureStub) HostBuild(context.Context, *pb.HostBuildRequest, ...grpc.CallOption) (*pb.HostBuildReply, error) {
	return nil, fmt.Errorf("ensureStub: HostBuild not implemented")
}
func (s *ensureStub) DescribeProvider(context.Context, *pb.DescribeProviderRequest, ...grpc.CallOption) (*pb.DescribeProviderReply, error) {
	return nil, fmt.Errorf("ensureStub: DescribeProvider not implemented")
}
func (s *ensureStub) Close() error { return nil }

// prepareOverlayBuild ensures every builder image its stages name BEFORE it creates the overlay dir,
// and refuses to continue when an ensure fails. Deleting the ensure loop leaves the stub unrecorded,
// so this test is what goes red on that change.
func TestPrepareOverlayBuildEnsuresBuilderImagesBeforeCreatingTheDir(t *testing.T) {
	work := t.TempDir()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(work); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(wd) })

	dir := t.TempDir()
	cand := deploykit.NewSpecCandyModel(spec.CandyModel{Name: "web", SourceDir: dir}, spec.CandyView{Name: "web"})
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	box := &buildkit.ResolvedBox{ResolvedBox: spec.ResolvedBox{Name: "base", Builder: buildkit.BuilderMap{"npm": "npm-builder"}},
		BuilderConfig: &spec.BuilderConfig{Builder: map[string]*spec.Builder{"npm": {DetectFiles: []string{"package.json"}}}}}
	builderBox := &buildkit.ResolvedBox{ResolvedBox: spec.ResolvedBox{Name: "npm-builder", FullTag: "ghcr.io/x/npm-builder:v1"}}
	dg := &deploykit.Generator{Boxes: map[string]*buildkit.ResolvedBox{"base": box, "npm-builder": builderBox}, Candies: map[string]deploykit.CandyModel{"web": cand}}

	stub := &ensureStub{deploy: "test"}
	overlayDir, err := prepareOverlayBuild(context.Background(), sdk.NewInProcExecutor(stub), dg, "base", "test", []string{"web"}, "podman", dir)
	if err != nil {
		t.Fatalf("prepareOverlayBuild: %v", err)
	}
	if len(stub.refs) == 0 {
		t.Fatalf("no builder image was ensured; the ensure-first loop is gone (needed %v)", dg.NeededBuilderImages("base", box, []string{"web"}))
	}
	for i, seen := range stub.dirSeen {
		if seen {
			t.Errorf("ensure call %d ran AFTER the overlay dir existed; the .build prune would delete it", i)
		}
	}
	if _, err := os.Stat(overlayDir); err != nil {
		t.Errorf("the overlay dir was not created: %v", err)
	}

	failing := &ensureStub{deploy: "test2", err: fmt.Errorf("boom")}
	if _, err := prepareOverlayBuild(context.Background(), sdk.NewInProcExecutor(failing), dg, "base", "test2", []string{"web"}, "podman", dir); err == nil {
		t.Error("a failed builder-image ensure did not stop the overlay build")
	}
}
