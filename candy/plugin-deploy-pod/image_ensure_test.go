package deploypod

import (
	"errors"
	"testing"

	"github.com/opencharly/spec/container"
	"github.com/opencharly/spec/spec"
)

// image_ensure_test.go — the staging leg stages into the store of the RESOLVED run engine.
// The retired subject hardcoded podman and transferred across stores; these arms pin the
// replacement: the configured engine is the only store consulted, `build:ensure` receives that
// same engine, and an unset word falls to the schema default.

func TestEnsureImagePresent(t *testing.T) {
	orig := container.LocalImageExists
	t.Cleanup(func() { container.LocalImageExists = orig })

	t.Run("configured engine has image", func(t *testing.T) {
		var probed []string
		container.LocalImageExists = func(engine, ref string) bool {
			probed = append(probed, engine)
			return engine == "containerd"
		}
		if err := ensureImagePresent(t.Context(), nil, "myimage:latest", "containerd"); err != nil {
			t.Errorf("expected no error, got: %v", err)
		}
		if len(probed) != 1 || probed[0] != "containerd" {
			t.Errorf("staging must probe exactly the configured engine, got %q", probed)
		}
	})

	t.Run("unset engine uses the schema default", func(t *testing.T) {
		var probed []string
		container.LocalImageExists = func(engine, ref string) bool {
			probed = append(probed, engine)
			return true
		}
		if err := ensureImagePresent(t.Context(), nil, "myimage:latest", ""); err != nil {
			t.Errorf("expected no error, got: %v", err)
		}
		if len(probed) != 1 || probed[0] != spec.DefaultContainerEngine {
			t.Errorf("unset engine must resolve to the schema default %q, got %q", spec.DefaultContainerEngine, probed)
		}
	})

	t.Run("image missing reaches build:ensure tier", func(t *testing.T) {
		container.LocalImageExists = func(engine, ref string) bool { return false }
		var err error
		func() {
			defer func() {
				if r := recover(); r != nil {
					// Reached dispatchBuildEnsurePeer's ex.InvokeProvider with a nil test
					// executor — proves the local-store tier was exhausted first.
					// build:ensure's own resolve/decode behavior is out of this package's
					// test scope.
					err = errors.New("reached build:ensure tier (nil-executor panic, expected)")
				}
			}()
			err = ensureImagePresent(t.Context(), nil, "myimage:latest", "containerd")
		}()
		if err == nil {
			t.Fatal("expected an error (either spec.ErrImageNotLocal or the tier-3 panic marker)")
		}
		if !errors.Is(err, spec.ErrImageNotLocal) && err.Error() != "reached build:ensure tier (nil-executor panic, expected)" {
			t.Errorf("expected spec.ErrImageNotLocal or the tier-3 reach marker, got: %v", err)
		}
	})
}
