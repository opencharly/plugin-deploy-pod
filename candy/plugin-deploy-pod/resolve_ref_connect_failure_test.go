package deploypod

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/opencharly/sdk"

	"github.com/opencharly/spec/spec"
)

// A transport/decode fault on either project-fallback leg is NOT the same as "this project
// declares no entry", but both used to return "" silently — so the box name degraded to the
// deploy KEY and the caller reported `image not found in local storage: <key>:<key>-<calver>`,
// naming a ref nothing ever built. These tests drive the two failure branches directly (a
// passing bed cannot reach them) and assert the cause is named on stderr while the degraded
// return value is preserved.

func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stderr = w
	done := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	fn()
	_ = w.Close()
	os.Stderr = orig
	return <-done
}

func TestDeployKeyToBoxLocal_ConnectFailureIsNamed(t *testing.T) {
	origConnect := projectConnect
	t.Cleanup(func() { projectConnect = origConnect })
	projectConnect = func(_ context.Context, _ *sdk.Executor, _ string, _ *spec.DeployPluginsConnectReply) error {
		return errors.New("wire shape mismatch")
	}

	var got string
	out := captureStderr(t, func() {
		got = deployKeyToBoxLocal(context.Background(), nil, nil, "check-devops-layer", "")
	})

	if got != "" {
		t.Fatalf("degraded return value changed: got %q, want %q", got, "")
	}
	for _, want := range []string{"Warning:", "check-devops-layer", "wire shape mismatch", "spec/sdk pins"} {
		if !strings.Contains(out, want) {
			t.Errorf("stderr does not name %q; got:\n%s", want, out)
		}
	}
}

func TestDeployKeyToBoxLocal_LoadFailureIsNamed(t *testing.T) {
	origConnect, origLoad := projectConnect, loadUnifiedForBox
	t.Cleanup(func() { projectConnect, loadUnifiedForBox = origConnect, origLoad })
	projectConnect = func(_ context.Context, _ *sdk.Executor, _ string, pre *spec.DeployPluginsConnectReply) error {
		pre.Dir = "/nonexistent/project"
		return nil
	}
	loadUnifiedForBox = func(_ context.Context, _ *sdk.Executor, _ string) (*spec.UnifiedFile, bool, error) {
		return nil, false, errors.New("decode failed")
	}

	var got string
	out := captureStderr(t, func() {
		got = deployKeyToBoxLocal(context.Background(), nil, nil, "check-devops-layer", "")
	})

	if got != "" {
		t.Fatalf("degraded return value changed: got %q, want %q", got, "")
	}
	for _, want := range []string{"Warning:", "check-devops-layer", "/nonexistent/project", "decode failed"} {
		if !strings.Contains(out, want) {
			t.Errorf("stderr does not name %q; got:\n%s", want, out)
		}
	}
}

// A leg that simply reports "no entry" must stay SILENT — otherwise every ordinary
// project without a fleet entry would start emitting warnings.
func TestDeployKeyToBoxLocal_NoEntryStaysSilent(t *testing.T) {
	origConnect := projectConnect
	t.Cleanup(func() { projectConnect = origConnect })
	projectConnect = func(_ context.Context, _ *sdk.Executor, _ string, pre *spec.DeployPluginsConnectReply) error {
		pre.Dir = "" // no project dir, no error
		return nil
	}

	out := captureStderr(t, func() {
		_ = deployKeyToBoxLocal(context.Background(), nil, nil, "check-devops-layer", "")
	})
	if strings.TrimSpace(out) != "" {
		t.Errorf("no-entry path must not warn; got:\n%s", out)
	}
}
