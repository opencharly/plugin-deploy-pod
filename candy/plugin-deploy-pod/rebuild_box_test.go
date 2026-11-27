package deploypod

// rebuild_box_test.go — `charly update <deploy> --build` must rebuild a box or refuse loudly
// (opencharly/charly#1297). The witness is the REFUSAL: a project that declares no box by the
// deploy's name must fail before anything runs, because the defect was a restart that looked
// like a rebuild.

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/opencharly/sdk"
	"github.com/opencharly/sdk/deploykit"
	"github.com/opencharly/spec/spec"
)

// stubProject replaces the executor-backed project loader for one test.
func stubProject(t *testing.T, uf *spec.UnifiedFile, ok bool, err error) {
	t.Helper()
	prevUF, prevLoad, prevConnect, prevLabel, prevRec := loadUnifiedForBox, loadDeploy, projectConnect, boxLabelOfRef, recordedImageOf
	t.Cleanup(func() {
		loadUnifiedForBox, loadDeploy, projectConnect, boxLabelOfRef, recordedImageOf = prevUF, prevLoad, prevConnect, prevLabel, prevRec
	})
	boxLabelOfRef = func(context.Context, *sdk.Executor, string) string { return "" }
	recordedImageOf = func(context.Context, *sdk.Executor, string, string) string { return "" }
	loadUnifiedForBox = func(context.Context, *sdk.Executor, string) (*spec.UnifiedFile, bool, error) {
		return uf, ok, err
	}
	// No host entry and no second project leg: these tests are about which DECLARED box is chosen.
	loadDeploy = func(context.Context, *sdk.Executor, string) (*deploykit.DeployConfig, error) {
		return nil, errors.New("no host config in this test")
	}
	projectConnect = func(_ context.Context, _ *sdk.Executor, _ string, pre *spec.DeployPluginsConnectReply) error {
		if err != nil {
			return err
		}
		pre.Dir = "/stub-project" // a resolvable project dir: the loader reads the DIR, not the box name
		return nil
	}
}

func TestResolveRebuildBoxName_DeclaredBox(t *testing.T) {
	stubProject(t, &spec.UnifiedFile{Box: spec.BoxMap{"whisper": nil}}, true, nil)
	got, err := resolveRebuildBoxName(context.Background(), nil, "whisper", "test")
	if err != nil {
		t.Fatalf("a declared box must resolve: %v", err)
	}
	if got != "whisper" {
		t.Fatalf("resolveRebuildBoxName = %q, want %q", got, "whisper")
	}
}

func TestResolveRebuildBoxName_InstanceQualifiedKeyUsesTheBase(t *testing.T) {
	stubProject(t, &spec.UnifiedFile{Box: spec.BoxMap{"whisper": nil}}, true, nil)
	got, err := resolveRebuildBoxName(context.Background(), nil, "whisper/deux", "test")
	if err != nil {
		t.Fatalf("a qualified key's base box must resolve: %v", err)
	}
	if got != "whisper" {
		t.Fatalf("resolveRebuildBoxName = %q, want %q (the key's base, never the instance-qualified key)", got, "whisper")
	}
}

// TestResolveRebuildBoxName_QualifiedRefUsesTheImageLabel is the steady-state shape measured on a
// disposable deploy: after any successful build or `charly config`, the entry records a QUALIFIED
// ref (`localhost/overlay-base:2026.331.1816`), so neither the bare form nor the deploy key names the
// box. The image's own ai.opencharly.box label does.
func TestResolveRebuildBoxName_QualifiedRefUsesTheImageLabel(t *testing.T) {
	stubProject(t, &spec.UnifiedFile{Box: spec.BoxMap{"overlay-base": nil}}, true, nil)
	recordedImageOf = func(context.Context, *sdk.Executor, string, string) string {
		return "localhost/overlay-base:2026.331.1816"
	}
	boxLabelOfRef = func(_ context.Context, _ *sdk.Executor, ref string) string {
		if strings.HasPrefix(ref, "localhost/") {
			return "overlay-base"
		}
		return ""
	}
	got, err := resolveRebuildBoxName(context.Background(), nil, "pod-overlay-builder", "test")
	if err != nil {
		t.Fatalf("a qualified recorded ref must resolve through the image's box label: %v", err)
	}
	if got != "overlay-base" {
		t.Fatalf("resolveRebuildBoxName = %q, want %q", got, "overlay-base")
	}
}

// TestRebuildBoxCandidate_BareImageNameBeatsTheKey is the bed shape: a bed's deploy key is
// `check-whisper-pod` while its recorded image is the BARE box name `whisper`, so the key alone
// would refuse a rebuild that has a perfectly good box.
func TestRebuildBoxCandidate_BareImageNameBeatsTheKey(t *testing.T) {
	declared := func(n string) bool { return n == "whisper" }
	// The recorded BARE name is the first candidate and needs no label read.
	if got, ok := rebuildBoxCandidate("whisper", "", "check-whisper-pod", declared); !ok || got != "whisper" {
		t.Fatalf("rebuildBoxCandidate = (%q,%v), want (whisper,true)", got, ok)
	}
	// A qualified recorded ref declares nothing; the IMAGE'S LABEL is the next candidate.
	if got, ok := rebuildBoxCandidate("localhost/whisper:2026.331.0001", "whisper", "check-whisper-pod", declared); !ok || got != "whisper" {
		t.Fatalf("rebuildBoxCandidate(qualified ref) = (%q,%v), want (whisper,true) via the label", got, ok)
	}
	// Then the key, when the ref and its label name nothing declared.
	if got, ok := rebuildBoxCandidate("ghcr.io/opencharly/x:1", "", "whisper", declared); !ok || got != "whisper" {
		t.Fatalf("rebuildBoxCandidate(key fallback) = (%q,%v), want (whisper,true)", got, ok)
	}
	// None declared: no candidate, never a guessed name.
	if got, ok := rebuildBoxCandidate("ghcr.io/opencharly/x:1", "y", "check-whisper-pod", declared); ok {
		t.Fatalf("rebuildBoxCandidate must refuse when no declared name matches, got %q", got)
	}
}

func TestResolveRebuildBoxName_UndeclaredBoxRefuses(t *testing.T) {
	stubProject(t, &spec.UnifiedFile{Box: spec.BoxMap{"other": nil}}, true, nil)
	got, err := resolveRebuildBoxName(context.Background(), nil, "pod-cdi-keepid-bind", "test")
	if err == nil {
		t.Fatalf("a box the project does not declare must be refused, got %q", got)
	}
	msg := err.Error()
	for _, want := range []string{"NOTHING WAS BUILT", "pod-cdi-keepid-bind", "charly box build"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("the refusal must say %q, got %q", want, msg)
		}
	}
	if got != "" {
		t.Fatalf("a refused resolution must return no box name, got %q", got)
	}
}

func TestResolveRebuildBoxName_ProjectFaultIsNamed(t *testing.T) {
	stubProject(t, nil, false, errors.New("reverse channel closed"))
	_, err := resolveRebuildBoxName(context.Background(), nil, "whisper", "test")
	if err == nil || !strings.Contains(err.Error(), "reverse channel closed") {
		t.Fatalf("a project-load fault must be named, not read as 'no box declared': %v", err)
	}
}

// TestPodRebuildOpts_DecodeTheHostWireShape is the change's own discriminator: the host marshals
// spec.DeployTargetRebuildOpts as rebuild_image/dry_run/tag, and the hand-rolled struct this
// replaced could not see `rebuild_image` at all — so `--build` decoded to false and the build leg
// never ran.
func TestPodRebuildOpts_DecodeTheHostWireShape(t *testing.T) {
	wire, err := json.Marshal(spec.DeployTargetRebuildOpts{RebuildImage: true, Tag: "2026.331.0001"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded spec.DeployTargetRebuildOpts
	if err := json.Unmarshal(wire, &decoded); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !decoded.RebuildImage {
		t.Fatalf("rebuild_image did not survive the wire round-trip: %s", wire)
	}
	if decoded.Tag != "2026.331.0001" {
		t.Fatalf("tag did not survive the wire round-trip: %s", wire)
	}
	// The retired shape, kept as the negative: its camel field cannot read the snake wire key.
	var retired struct {
		RebuildImage bool `json:"RebuildImage"`
	}
	if err := json.Unmarshal(wire, &retired); err != nil {
		t.Fatalf("decode retired: %v", err)
	}
	if retired.RebuildImage {
		t.Fatal("the retired camel-only field read a snake_case wire key — if this ever passes, the discriminator is dead")
	}
}
