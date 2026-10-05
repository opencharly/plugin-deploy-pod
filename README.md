# plugin-deploy-pod

The `pod` deploy substrate for OpenCharly — `target: pod`, the DEFAULT substrate
(a deployment run as a container image via quadlet/podman), served
out-of-process (`deploy:pod`).

The plugin is a standalone Go module the charly loader host-builds and serves
over go-plugin gRPC. It is the pod-substrate sibling of `plugin-deploy-vm`.

## What it provides

| Capability | Surface |
|---|---|
| `deploy:pod` | the `pod:` deploy substrate (the default target) |

## How it works

Unlike `deploy:vm` (whose plugin WALKS the plan inside the guest), pod bakes its
install steps INTO the image at BUILD time:

- The pod lifecycle (`lifecycle.go`, this plugin) builds the overlay container
  image HOST-SIDE in `PrepareVenue`: the host prep returns the `OverlayBuildReply`
  envelope (resolved project + plans + base-image metadata + per-overlay-candy
  security + parent bind-mount volumes), and the candy imports
  `sdk/deploykit`+`buildkit`+`kit` directly, constructs the `deploykit.Generator`
  itself, renders the overlay Containerfile in its own code, and runs
  `podman build` + the deploy-name alias tag via its served host executor. Each
  per-step Containerfile fragment is rendered host-side over the generic
  `step-emit` host-builder.
- So there is NO per-step venue walk for pod: this plugin's `Invoke` returns an
  EMPTY `DeployReply` — pod teardown is `charly remove` + drop overlay images,
  owned by the host lifecycle hook's `PostTeardown`.
- The pod config-WRITE also lives here: `OpConfigWrite` renders the
  quadlet/`.pod`/sidecar/tunnel file contents and writes them at the host-resolved
  absolute paths + exact modes.

## How to use it

Compose the plugin candy in a project's `candy:` list:

```yaml
- '@github.com/opencharly/plugin-deploy-pod/candy/plugin-deploy-pod:<tag>'
```

Then author a `pod:` deploy (the default substrate):

```yaml
my-deploy:
    pod:
        image: my-box
```

## Layout

- `candy/plugin-deploy-pod/` — the plugin module: `plugin.go` (the deploy
  provider + `NewProvider()` / `NewMeta()` / `Invoke`), `lifecycle.go` (the
  overlay build + venue lifecycle ops), `config_write.go` /
  `config_setup.go` / `config_remove.go` (the config surface), `resolve*.go`
  (the project/ref/sidecar resolution), `schema/pod.cue` (the self-contained
  `#DeployPodPlugin`), the Go tests, `cmd/serve/main.go`.
- `charly.yml` — the root project manifest (`discover: candy`).
- `.github/workflows/tag-on-merge.yml` — CalVer tag + `CHANGELOG/` on merge.

## Related

- Owning skill: `/charly-pod:pod` — the `kind: pod` / deploy entity schema
  reference.
- `/charly-core:deploy` — `charly deploy add`/`del`, quadlet generation, tunnels,
  and per-machine deploy overlays.
- `/charly-internals:plugin` — the plugin/provider model, including the `deploy`
  provider class.
- [`opencharly/charly`](https://github.com/opencharly/charly) — the charly CLI.
