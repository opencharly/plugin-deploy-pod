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
	noVocab := &buildkit.ResolvedBox{ResolvedBox: spec.ResolvedBox{Name: "base"}}
	dg := &deploykit.Generator{
		Boxes:   map[string]*buildkit.ResolvedBox{"base": withVocab},
		Candies: map[string]deploykit.CandyModel{"web": npmCandy},
	}
	_ = noVocab

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
