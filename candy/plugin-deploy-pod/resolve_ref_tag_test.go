package deploypod

// resolve_ref_tag_test.go — opencharly/charly#1323: a `--tag` on a deploy that records a FULL
// image ref must REPLACE the ref's own tag, not append a second one.

import "testing"

func TestRefWithoutTag(t *testing.T) {
	cases := map[string]string{
		"ghcr.io/opencharly/whisper:2026.325.2318": "ghcr.io/opencharly/whisper",
		"ghcr.io/opencharly/whisper":               "ghcr.io/opencharly/whisper",
		"ghcr.io/opencharly/whisper@sha256:abc123": "ghcr.io/opencharly/whisper",
		// A registry PORT is not a tag: the separator is only the LAST ':' after the last '/'.
		"localhost:5000/opencharly/whisper:2026.325.2318": "localhost:5000/opencharly/whisper",
		"localhost:5000/opencharly/whisper":               "localhost:5000/opencharly/whisper",
	}
	for in, want := range cases {
		if got := refWithoutTag(in); got != want {
			t.Fatalf("refWithoutTag(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestResolveDeployImageRefReplacesTheRecordedTag is the change's own discriminator: a full ref
// that carries a tag plus a new tag must come back carrying ONLY the new one. Before the fix this
// produced `…:2026.325.2318:2026.332.0936` — the invalid reference the engine refused.
func TestResolveDeployImageRefReplacesTheRecordedTag(t *testing.T) {
	got := resolveDeployImageRef(t.Context(), nil, "whisper",
		"ghcr.io/opencharly/whisper:2026.325.2318", "2026.332.0936")
	if want := "ghcr.io/opencharly/whisper:2026.332.0936"; got != want {
		t.Fatalf("resolveDeployImageRef = %q, want %q (a tag REPLACES the recorded one)", got, want)
	}
}
