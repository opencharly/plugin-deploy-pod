package deploypod

import (
	"context"

	"github.com/opencharly/sdk"
	"github.com/opencharly/sdk/deploykit"
	"github.com/opencharly/sdk/kit"
	"github.com/opencharly/spec/container"
	"github.com/opencharly/spec/spec"
)

// selection.go — the pod substrate's ONE consumer of the sdk selection model. Every site in this
// package that needs to know which engine a deploy runs on calls resolvePodSelection; none reads
// rt.RunEngine, re-derives the deploy-entry-over-runtime precedence by hand, or carries an engine
// word literal.
//
// The precedence is the sdk's (flag > deploy entity > env > settings file > built-in default).
// This package has no engine FLAG, so the flag level is always empty; the deploy entity comes
// from the per-host overlay entry, and the env/settings fold arrives already resolved on
// rt.RunEngine / rt.Namespace (hostenv.ResolveRuntime owns it). `auto` is a selector the resolver
// handles at its own level.
//
// The candy `engine:` constraint has no source on this side of the plugin boundary: the deploy
// plugin receives a deploy node, not the resolved candy graph. It is therefore left empty here,
// exactly as the sibling deploy plugin does; the constraint stays enforced where the graph exists
// (`charly deploy add`).

// podSelection is the resolved selection for one deploy: the engine word and the engine target
// facts (address, namespace, rootful, privilege prefix) that a run plan, unit emission and image
// store need.
type podSelection struct {
	Engine spec.EngineName
	Target deploykit.ContainerdTarget
}

// binary is the engine's CLI binary. It is a capability fact (containerd is driven through
// nerdctl), never an engine-word spelling.
func (s podSelection) binary() string { return kit.EngineBinary(string(s.Engine)) }

// deploySelection maps the deploy entity level of the precedence chain onto the resolver's
// Selection. An absent entry contributes nothing, so every lower level is consulted.
func deploySelection(dc *deploykit.DeployConfig, box, instance string) deploykit.Selection {
	entry, ok := dc.Lookup(box, instance)
	if !ok {
		return deploykit.Selection{}
	}
	return deploykit.Selection{
		Engine:    entry.Engine,
		Rootful:   entry.Rootful,
		Namespace: entry.Namespace,
	}
}

// resolvePodSelection runs the ONE selection model: the engine word first, then the target facts
// derived from that word. ResolveContainerdTarget receives the already-resolved engine, so the
// address/namespace/rootful facts can never disagree with the engine that launches.
func resolvePodSelection(deploy deploykit.Selection, rt *kit.ResolvedRuntime) (podSelection, error) {
	engine, err := deploykit.ResolvePodEngine("", deploy, rt)
	if err != nil {
		return podSelection{}, err
	}
	target, err := deploykit.ResolveContainerdTarget(engine, deploy, rt)
	if err != nil {
		return podSelection{}, err
	}
	return podSelection{Engine: engine, Target: target}, nil
}

// resolvePodSelectionFromConfig resolves against an already-loaded overlay.
func resolvePodSelectionFromConfig(dc *deploykit.DeployConfig, box, instance string, rt *kit.ResolvedRuntime) (podSelection, error) {
	return resolvePodSelection(deploySelection(dc, box, instance), rt)
}

// resolvePodSelectionFor loads the per-host overlay once and resolves against it. Callers that
// already hold the config use resolvePodSelectionFromConfig instead.
func resolvePodSelectionFor(ctx context.Context, ex *sdk.Executor, box, instance string, rt *kit.ResolvedRuntime) (podSelection, error) {
	dc, err := loadDeploy(ctx, ex, "resolve pod selection")
	if err != nil {
		return podSelection{}, err
	}
	return resolvePodSelectionFromConfig(dc, box, instance, rt)
}

// detectRunMode is the host-availability half of runtimeForSelection. It is a package var so a
// unit test can pin the capability-table answer without depending on the runner's systemd-user
// session; production reads the ONE host probe in spec/hostenv.
var detectRunMode = kit.DetectRunMode

// runtimeForSelection returns rt with the persistence/supervision MODE re-derived from the
// deploy's resolved engine. rt.RunMode was resolved for rt.RunEngine (the host-wide engine.run:
// env > settings > detection); a deploy-level `engine:` overrides that WORD, and the mode is a
// capability FACT of the selected word — containerd persists as a `systemd-unit`, podman as a
// quadlet, docker direct. Keeping the global mode emits a podman quadlet for a containerd deploy
// and `charly start` then runs podman (opencharly/charly#1109).
//
// `direct` is engine-agnostic (the engine CLI is executed with no unit), so an explicit or
// host-degraded direct mode is preserved. A unit mode is re-derived through the same
// capability+availability rule ResolveRuntime uses, so a host without a systemd-user session
// still degrades to direct. An unchanged engine, a nil rt, or an unresolved selection is a
// no-op.
func runtimeForSelection(rt *kit.ResolvedRuntime, sel podSelection) *kit.ResolvedRuntime {
	if rt == nil || sel.Engine == "" || string(sel.Engine) == rt.RunEngine {
		return rt
	}
	out := *rt
	out.RunEngine = string(sel.Engine)
	if container.IsUnitRunMode(out.RunMode) {
		out.RunMode = detectRunMode(string(sel.Engine))
	}
	return &out
}
