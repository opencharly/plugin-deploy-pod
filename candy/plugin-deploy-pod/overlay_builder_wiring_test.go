package deploypod

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/opencharly/sdk/buildkit"
	"github.com/opencharly/sdk/deploykit"
	"github.com/opencharly/spec/spec"
)

// The overlay's builder wiring goes through the Generator the image build uses: a candy that needs a
// builder whose stage was never resolved must FAIL with the Generator's own error, and a box with no
// builder image configured must fail the pre-pass. Both outcomes can only come from
// EmitBuilderStages actually being called — an overlay that skipped the wiring would return empty
// strings and no error.
func TestEmitOverlayBuildersDrivesTheGeneratorsBuilderPath(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	npmCandy := deploykit.NewSpecCandyModel(spec.CandyModel{Name: "web", SourceDir: dir}, spec.CandyView{Name: "web"})
	withVocab := &buildkit.ResolvedBox{ResolvedBox: spec.ResolvedBox{Name: "base"},
		BuilderConfig: &spec.BuilderConfig{Builder: map[string]*spec.Builder{"npm": {DetectFiles: []string{"package.json"}}}}}
	dg := &deploykit.Generator{
		Boxes:   map[string]*buildkit.ResolvedBox{"base": withVocab},
		Candies: map[string]deploykit.CandyModel{"web": npmCandy},
	}

	if _, _, err := emitOverlayBuilders(dg, "base", nil, []string{"web"}); err != nil {
		t.Errorf("no box means no builder wiring; got %v", err)
	}

	// The candy needs npm, and no stage reply was cached for it: the wiring must surface the
	// Generator's refusal, named with the candy and the builder.
	stages, _, err := emitOverlayBuilders(dg, "base", withVocab, []string{"web"})
	if err == nil {
		t.Fatalf("a needed builder with no resolved stage was accepted (stages=%q)", stages)
	}
	if !strings.Contains(err.Error(), "web") || !strings.Contains(err.Error(), "npm") {
		t.Errorf("the refusal does not name the candy and the builder: %v", err)
	}
}

// With a resolved builder stage, the wiring emits BOTH halves: the stage and the payload COPY that
// brings the builder's output into the overlay image. Deleting only the EmitBuilderArtifacts call
// leaves the stages in place, so this test — not the refusal test above — is what catches it.
func TestEmitOverlayBuildersEmitsThePayloadCopyWhenAStageResolves(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	cand := deploykit.NewSpecCandyModel(spec.CandyModel{Name: "web", SourceDir: dir}, spec.CandyView{Name: "web"})
	box := &buildkit.ResolvedBox{ResolvedBox: spec.ResolvedBox{Name: "base", Builder: buildkit.BuilderMap{"npm": "npm-builder"}},
		BuilderConfig: &spec.BuilderConfig{Builder: map[string]*spec.Builder{"npm": {DetectFiles: []string{"package.json"}}}}}
	builderBox := &buildkit.ResolvedBox{ResolvedBox: spec.ResolvedBox{Name: "npm-builder", FullTag: "ghcr.io/x/npm-builder:v1"}}
	dg := &deploykit.Generator{
		Boxes:   map[string]*buildkit.ResolvedBox{"base": box, "npm-builder": builderBox},
		Candies: map[string]deploykit.CandyModel{"web": cand},
		// The pre-pass refuses a stage builder whose plugin is not available; this test supplies the
		// resolver itself, so it declares the builder externalized, exactly as the host does.
		ExternalizedBuilders: map[string]bool{"npm": true},
	}
	dg.ResolveDetectionBuilderStage = func(_ string, in spec.BuilderResolveInput, _ *spec.ResolvedBox) (spec.BuilderResolveReply, error) {
		return spec.BuilderResolveReply{
			Stage:         "FROM " + in.BuilderRef + " AS " + in.StageName,
			CopyArtifacts: []string{"COPY --from=" + in.StageName + " --chown=1000:1000 /home/user /home/user"},
		}, nil
	}

	stages, artifacts, err := emitOverlayBuilders(dg, "base", box, []string{"web"})
	if err != nil {
		t.Fatalf("emitOverlayBuilders: %v", err)
	}
	if !strings.Contains(stages, "AS ") || !strings.Contains(stages, "npm-builder:v1") {
		t.Errorf("no builder stage was emitted: %q", stages)
	}
	if !strings.Contains(artifacts, "COPY --from=") || !strings.Contains(artifacts, "/home/user") {
		t.Errorf("no payload COPY was emitted: %q", artifacts)
	}
}
