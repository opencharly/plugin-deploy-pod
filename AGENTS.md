# AGENTS.md — plugin-deploy-pod

Standalone out-of-tree DEPLOY plugin repo serving the `pod` deploy substrate
(`deploy:pod`, the default target). The plugin is a Go module at
`candy/plugin-deploy-pod/` (module path
`github.com/opencharly/plugin-deploy-pod/candy/plugin-deploy-pod`); the root
`charly.yml` only declares `discover: candy` so the repo is a project and its
candy is scanned.

Canonical files:

- `candy/plugin-deploy-pod/charly.yml` — the `plugin-deploy-pod:` candy entity
  (`plugin:` block, `plan:` check).
- `candy/plugin-deploy-pod/plugin.go` — the deploy provider (`NewProvider()` /
  `NewMeta()` / the `Invoke` dispatch across `OpConfigWrite` / `OpConfigSetup` /
  `OpConfigRemove` / the lifecycle ops).
- `candy/plugin-deploy-pod/lifecycle.go` — the overlay build + venue lifecycle
  ops.
- `candy/plugin-deploy-pod/config_write.go` / `config_setup.go` /
  `config_remove.go` — the config surface.
- `candy/plugin-deploy-pod/schema/pod.cue` — the self-contained
  `#DeployPodPlugin`.
- `charly.yml` — the root project manifest (`discover: candy`).
- `.github/workflows/tag-on-merge.yml` — CalVer tag + `CHANGELOG/` on merge.
- `README.md` — user overview only; never agent guidance.

## Load these skills first (R0)

- `/charly-pod:pod` — the `kind: pod` / deploy entity schema reference. Load
  before changing the substrate or the deploy entity shape. This candy carries
  no `skill:` entity of its own; the gap is tracked in
  [opencharly/opencharly#291](https://github.com/opencharly/opencharly/issues/291).
- `/charly-core:deploy` — `charly deploy add`/`del`, quadlet generation, volume
  backing, tunnels, and per-machine deploy overlays.
- `/charly-internals:plugin` — the plugin authoring reference: the `plugin:`
  block, the `deploy` provider class, the per-plugin CUE-schema contract.
- `/charly-internals:git-workflow` — before any git/PR action.

## Build / validate / test

- `go build ./...` in `candy/plugin-deploy-pod/` — compile the plugin module.
- `go test ./...` in `candy/plugin-deploy-pod/` — the plugin's Go tests (the
  config setup/remove/write + resolve seams).
- `charly box validate` at the repo root — the structural check (the candy +
  `plugin:` block, CUE schema).
- The merge gate is the **org-wide** `charly/pr-validator` (required check
  `validate / validate`, defined in `opencharly/.github`); this repo has **no**
  per-repo candy gate.
- The R10 consumers are the pod beds (`check-pod-overlay`, `check-sidecar-pod`),
  run to a fresh `charly update`.

## Modify this repo

- Edit the `plugin-deploy-pod:` candy entity, the Go source, and
  `schema/pod.cue` **together** — the schema is the served declaration surface.
- Pod bakes its install steps into the overlay image HOST-SIDE: keep the plan
  walk OUT of `Invoke` (walking add_candy steps on a host venue would be wrong).
  Pod teardown stays `charly remove` + drop overlay images.
- The overlay render lives here, not in core: keep the `deploykit.Generator`
  construction shared with `candy/plugin-build` (R3).

## Landing

- PR-only. Every change lands through a pull request; the org-required
  `charly/pr-validator` validates the diff and body and arms native auto-merge on
  PASS. Direct pushes to `main` are blocked.
- History lives in `CHANGELOG/` (written by `tag-on-merge` at merge time); the PR
  body IS the changelog.
- The authoritative rulebook is the umbrella `AGENTS.md` in
  `opencharly/opencharly` and `charly/AGENTS.md` in the charly repo. Do not
  restate its rules here.
