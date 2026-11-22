package deploypod

import (
	"strings"
	"testing"
)

// A pod overlay renders its candies' builders like the box build does (opencharly/sdk#608): every
// builder stage is opened BEFORE the main FROM, and each builder's payload is copied in AFTER it,
// ahead of the candy tasks. A builder stage opened after the main FROM becomes the final image and
// copies nothing back into the overlay, which is the defect this order prevents.
func TestOverlayMainStageHoistsBuilderStagesAndCopiesPayloadsAfterTheMainFrom(t *testing.T) {
	const (
		svc      = "FROM scratch AS svc\nCOPY svc/ /\n\n"
		stage    = "FROM ghcr.io/x/arch-builder:v1 AS cowsay-npm-build\nRUN npm install\n\n"
		base     = "ghcr.io/x/pod-base:v1"
		artifact = "COPY --from=cowsay-npm-build --chown=1000:1000 /home/user /home/user\n"
		tasks    = "RUN echo task\n"
	)
	cf := overlayMainStage(svc, stage, base, artifact, tasks)

	mainFrom := strings.Index(cf, "FROM "+base+"\n")
	if mainFrom < 0 {
		t.Fatalf("no main FROM %s in:\n%s", base, cf)
	}
	if i := strings.Index(cf, "AS cowsay-npm-build"); i < 0 || i > mainFrom {
		t.Errorf("the builder stage opens at %d, want before the main FROM at %d:\n%s", i, mainFrom, cf)
	}
	if i := strings.Index(cf, "AS svc"); i < 0 || i > mainFrom {
		t.Errorf("the service stage opens at %d, want before the main FROM at %d:\n%s", i, mainFrom, cf)
	}
	userRoot := strings.Index(cf, "USER root")
	copyAt := strings.Index(cf, artifact)
	taskAt := strings.Index(cf, tasks)
	if userRoot <= mainFrom || copyAt <= userRoot || taskAt <= copyAt {
		t.Errorf("want main FROM (%d) < USER root (%d) < builder payload (%d) < candy tasks (%d):\n%s", mainFrom, userRoot, copyAt, taskAt, cf)
	}
	if strings.Count(cf, "FROM "+base) != 1 {
		t.Errorf("the main FROM appears %d times, want once:\n%s", strings.Count(cf, "FROM "+base), cf)
	}
}
