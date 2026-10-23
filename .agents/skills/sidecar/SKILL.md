---
name: sidecar
description: |-
  Sidecars configured through `charly config` and `charly.yml` — sidecar containers, pod networking, Tailscale exit nodes, sidecar flags, or `env_accept`/`env_require` routing.
---

# Sidecar — Deploy-Time Pod Composition

## Overview

Sidecars are additional containers that run alongside an application container in a shared Podman pod. They share the network namespace (localhost connectivity) while maintaining separate filesystems. Sidecar templates are embedded in the `charly` binary — any image can get any sidecar at deploy time without image rebuilds.

## Quick Reference

| Action | Command |
|--------|---------|
| List available sidecars | `charly config --list-sidecars` |
| Attach sidecar | `charly config <image> --sidecar tailscale` |
| Attach with env | `charly config <image> --sidecar tailscale -e TS_HOSTNAME=my-app` |
| Check sidecar status | `charly cmd <image> --sidecar tailscale "tailscale status"` |

## Architecture

Sidecar templates are compiled into the `charly` binary via `go:embed` — they are name-first `sidecar`-kind nodes in the embedded default `charly/charly.yml` (plain node-form YAML), parsed through the SAME unified loader as any project `charly.yml` (folded into `UnifiedFile.Sidecar`). A project may also declare its own `sidecar`-kind nodes that extend/override the embedded set. At deploy time, `charly config --sidecar <name>` merges the embedded template with the project's templates and the per-machine overrides from `charly.yml`, then generates quadlet files.

```
charly binary (embedded templates)     charly.yml (per-machine overrides)
        ↓                                    ↓
   sidecar templates              env overrides + secrets
        ↓ MergeSidecars()                    ↓
              ResolveSidecars()
                    ↓
             pod + sidecar + app quadlets
```

### Generated Files

When sidecars are attached, `charly config` generates **3 files** instead of 1:

| File | Purpose |
|------|---------|
| `charly-<image>.pod` | Pod definition: `Network=charly`, ports (`PodmanArgs=-p`), `--shm-size` |
| `charly-<image>-<sidecar>.container` | Sidecar: image, env, caps, devices, secrets, volumes |
| `charly-<image>.container` | App: `Pod=charly-<image>.pod`, no ports/network (pod owns them) |

### Dual Networking

The pod stays on the "charly" bridge network (`Network=charly` in `.pod` file) for container-to-container connectivity. The Tailscale sidecar creates a `tailscale0` tun interface for exit node routing. `--exit-node-allow-lan-access` adds a routing exception (`throw 10.89.0.0/24`) that keeps bridge traffic on the bridge.

```
charly bridge (container-to-container)
┌─────────────────────────────────────┐
│  charly-<image> pod (Network=charly)        │
│  ┌───────────┐  ┌────────────────┐  │
│  │ tailscale │  │ app container  │  │
│  │ sidecar   │  │ :3000, :9222   │  │
│  │ tailscale0│  │                │  │
│  │ (tun)     │  │                │  │
│  └─────┬─────┘  └────────────────┘  │
└────────┼────────────────────────────┘
         │ outbound internet only
    exit node
```

### Env Var Routing

CLI `-e KEY=VALUE` flags are automatically routed: env vars matching a sidecar template's keys (all `TS_*` for tailscale) go to the sidecar's charly.yml env override. Other vars go to the app container.

## Environment Contract (env_provide / env_accept / env_require)

Sidecars participate in the same cross-container env discovery pipeline as regular candies, with one critical caveat: **routing is explicit, not implicit**. A sidecar's env (e.g., the tailscale sidecar's `TS_*` vars) is **not** auto-injected into the app container, and vice versa — the app only sees what it explicitly opts in to via `env_accept` or `env_require` in its `charly.yml`.

This matters for two reasons:

1. **Prevents env var leakage.** Without opt-in filtering, every deployed service would see every other service's `env_provide`. The chrome candy doesn't want `TS_*` vars in its env; the tailscale sidecar doesn't want `BROWSER_CDP_URL`. The filtering model is the mechanism that enforces this boundary.

2. **Enforces dependency contracts.** When the app declares `env_require` and the provider (or sidecar) is actually deployed, the provide-resolution pipeline (`provides.go`) satisfies the requirement without the user manually setting `-e` flags. When the provider is **not** deployed and no default is set, `charly config` fails hard — deployment does not proceed with a broken env contract.

### Tailscale sidecar as a provides participant

The tailscale sidecar declares its advertised state (e.g., `TS_HOSTNAME`, the sidecar's tailnet IP) via the sidecar template, not via `env_provide`. The app container receives only what it declares in `env_accept`. Most images don't need to look at tailscale state from inside the container — the sidecar handles all routing transparently — so the accepts set is usually empty.

If a future sidecar needs to forward auth tokens or service URLs into the app, the right pattern is:

- Sidecar template declares `env_provide:` with `{{.ContainerName}}`-templated values
- App candy declares `env_accept: [<var>]` or `env_require: [<var>]`
- `charly config` resolves the provide at deploy time and writes it to `charly.yml` under `provides:`

Missing `env_accept` on the consumer side silently drops the var. Missing `env_require` is a hard fail. See `/charly-image:layer` (env_require / env_accept) for the authoring side, `/charly-core:charly-config` (Provides Filtering) for the resolution pipeline, and `provides.go` in the `charly` source for the implementation.

### charly.yml Persistence

`--sidecar` assignments and `-e` overrides are saved to `charly.yml`. Subsequent `charly config` calls re-read them:

```yaml
selkies-desktop:
  pod:
    image: selkies-desktop
    sidecar:
      tailscale:
        env:
          TS_HOSTNAME: selkies-desktop
          TS_EXTRA_ARGS: "--exit-node=100.80.254.4 --exit-node-allow-lan-access"
```

## Tailscale Sidecar

The built-in tailscale sidecar template (validated against `tailscale/tailscale` containerboot source):

### Multi-tailnet auth-key store

The sidecar is parameterized on a `tailnet:` field declaring the target
tailnet's MagicDNS suffix. Each tailnet's auth-key lives in `.secrets`
(GPG-encrypted env file, loaded by direnv) under a per-tailnet env var
name derived from the suffix.

**Schema in the embedded `charly/charly.yml` (the name-first `tailscale` sidecar node):**

```yaml
tailscale:
  sidecar:
    parameter:
      tailnet: ""            # required: deploy must supply via parameter.tailnet
    secret:
      - name: ts-authkey
        env: TS_AUTHKEY                                                     # container var (what tailscale's binary reads)
        env_from: "TS_AUTHKEY_{{.Parameter.tailnet | tailnetEnvSuffix}}"    # host-side var (what charly reads from .secrets)
```

**Deploy shape (a deploy of the `sway-browser-vnc` box as the `ecovoyage` instance):**

```yaml
ecovoyage:
  pod:
    image: sway-browser-vnc
    sidecar:
      tailscale:
        parameter:
          tailnet: armadillo-quail.ts.net   # picks which per-tailnet auth-key to use
        env:
          TS_HOSTNAME: ecovoyage-browser
          TS_ACCEPT_DNS: "true"
```

**Storage convention (`.secrets`):** the `tailnetEnvSuffix` template
helper uppercases the MagicDNS suffix and replaces every non-alphanumeric
character with `_`. So:

| MagicDNS suffix | Resolved host env var |
|---|---|
| `armadillo-quail.ts.net` | `TS_AUTHKEY_ARMADILLO_QUAIL_TS_NET` |
| `tail297eca.ts.net` | `TS_AUTHKEY_TAIL297ECA_TS_NET` |
| `acme-corp.example.com` | `TS_AUTHKEY_ACME_CORP_EXAMPLE_COM` |

**Operator workflow — add a new tailnet:**

```bash
# 1. Generate an auth-key in the Tailscale admin console, signed in as
#    the OWNING ACCOUNT for the target tailnet. Verify which account
#    you're in by checking the top-right email. The MagicDNS suffix is
#    visible at https://login.tailscale.com/admin/dns
#
#    Set: reusable=yes, ephemeral=yes, preauthorized=yes,
#    tags=tag:server (or your ACL), expiration=90d (preference).

# 2. Store under the per-tailnet env var name:
charly secrets gpg set TS_AUTHKEY_ARMADILLO_QUAIL_TS_NET tskey-auth-XXXXXXXXX

# 3. Wire `parameter.tailnet:` into charly.yml under sidecar.tailscale:
#    (operator-edited; no auto-write)

# 4. Wipe stale state so the sidecar re-auths with the new key:
charly volume reset <image> tailscale-state [-i <instance>]

# 5. Apply:
charly stop <image> [-i <instance>]
charly config <image> [-i <instance>]
charly start <image> [-i <instance>]

# 6. Verify the right tailnet was joined:
charly cmd <image> --sidecar tailscale "tailscale status --json" [-i <instance>] \
    | python3 -c 'import json,sys; r=json.load(sys.stdin); print(r["MagicDNSSuffix"])'
# Expected: armadillo-quail.ts.net
```

**Migrating a legacy single-`TS_AUTHKEY` config:**

A legacy flat config used a single `TS_AUTHKEY` env var, which can't
distinguish multiple tailnets. `charly migrate` upgrades it to the per-tailnet
form:

```bash
charly migrate
# Prompts for the tailnet the existing TS_AUTHKEY belongs to (auto-detects
# from a running sidecar when present), renames the .secrets entry to the
# per-tailnet form, and warns about charly.yml entries that need
# parameter.tailnet set.
```

Idempotent. Note there are no `--tailnet` or `--delete-legacy` flags — `charly
migrate` is a single idempotent chain, so the tailnet comes from the prompt (or the
auto-detected running sidecar) and the legacy `TS_AUTHKEY` entry is left in place for
you to remove yourself once the per-tailnet entry is confirmed.

Any deploy with a tailscale sidecar that doesn't supply
`parameter.tailnet:` fails at `charly config` time with the message:

> sidecar "tailscale": sidecar secret "ts-authkey" references parameter "tailnet" which is unset. Set `sidecars.<sidecar-name>.parameter.tailnet: <value>` in charly.yml or run `charly migrate`

### Environment Variables

| Env var | Default | Purpose |
|---------|---------|---------|
| `TS_STATE_DIR` | `/var/lib/tailscale` | Persistent state (auth, node identity) |
| `TS_AUTH_ONCE` | `true` | Skip re-auth when state exists |
| `TS_USERSPACE` | `false` | **Kernel mode** — required for exit node routing |
| `TS_DEBUG_FIREWALL_MODE` | `nftables` | Force nftables (iptables-legacy fails in rootless podman) |
| `TS_ACCEPT_DNS` | `false` | Prevents Tailscale from rewriting `/etc/resolv.conf`. Pod quadlet adds explicit `--dns` flags for container DNS + MagicDNS |
| `TS_ENABLE_HEALTH_CHECK` | `true` | `/healthz` endpoint |
| `TS_LOCAL_ADDR_PORT` | `[::]:9002` | Health check listen address |
| `TS_AUTHKEY` | Via secret | Auth key (from credential store) |
| `TS_HOSTNAME` | Per-image | Tailscale device name (set via `-e`) |
| `TS_EXTRA_ARGS` | Per-image | Extra `tailscale up` flags (e.g., `--exit-node=<ip>`) |

### Security

- **Capabilities:** `NET_ADMIN` (iptables/nftables, IP forwarding), `SYS_MODULE` (tun/tap kernel module)
- **Devices:** `/dev/net/tun` (TUN/TAP virtual network device)

### State & Secrets

- **Volume:** `charly-<image>-tailscale-state` at `/var/lib/tailscale` — node identity persists across restarts
- **Secret:** `charly-<image>-tailscale-ts-authkey` — provisioned as podman secret from `TS_AUTHKEY` env var (loaded from `.secrets` via `charly secrets gpg env`)

### Exit Node Routing

```bash
# Store auth key for the sidecar's tailnet
charly secrets gpg set TS_AUTHKEY tskey-auth-xxxxxxxxxxxx

# Deploy with exit node
charly config selkies-desktop --sidecar tailscale \
  -e TS_HOSTNAME=selkies-desktop \
  -e "TS_EXTRA_ARGS=--exit-node=100.80.254.4 --exit-node-allow-lan-access"
charly start selkies-desktop

# First time only: exit node must be set via tailscale set
# (TS_EXTRA_ARGS only applies on first auth, not restarts)
charly cmd selkies-desktop --sidecar tailscale \
  "tailscale set --exit-node=100.80.254.4 --exit-node-allow-lan-access"

# Verify: pod shows exit node's IP, not host's
charly cmd selkies-desktop "curl -s ifconfig.me"
```

**Prerequisites:**
- Exit node device must advertise as exit node (`tailscale set --advertise-exit-node` on the device)
- Exit node must be **approved** on the sidecar's tailnet admin console
- Exit node IP is the **Tailscale IP** (e.g., `100.80.254.4`), not a public IP

**Persistence:** `tailscale set --exit-node` persists in the state volume. Survives pod restarts without re-configuration.

### Different Tailnet from Host

The sidecar runs its own `tailscaled` daemon in the pod's network namespace with its own state volume. It is completely isolated from the host's Tailscale:

| Aspect | Host Tailscale | Sidecar Tailscale |
|--------|---------------|-------------------|
| State | `/var/lib/tailscale` | Named volume |
| Auth key | Host's | Sidecar's `TS_AUTHKEY` |
| Network NS | Host | Pod (shared with app) |
| Control server | Default | Configurable via `TS_EXTRA_ARGS=--login-server=...` |

### Host Tailnet Port Exposure

The host's `tunnel: tailscale` (configured in charly.yml) is independent of the sidecar:
- `ExecStartPost=tailscale serve` runs on the **host**, exposing pod ports on the **host's tailnet**
- The sidecar handles routing on the **sidecar's tailnet**
- Both work simultaneously (dual networking)

### Pod ShmSize

Chrome requires large `/dev/shm`. In pod mode, per-container `ShmSize=` is ignored (pod infra container owns `/dev/shm`). The pod quadlet propagates ShmSize via `PodmanArgs=--shm-size=1g`.

## Cross-References

- `/charly-core:deploy` — Quadlet generation, charly.yml, tunnel configuration (tunnel is charly.yml-only, not auto-inherited by instances)
- `/charly-core:charly-config` — `--sidecar` and `--list-sidecars` flags, Provides Filtering, resource caps, NO_PROXY auto-enrichment
- `/charly-image:layer` — `env_accept` / `env_require` authoring and the full provides filtering contract
- `/charly-build:secrets` — `charly secrets gpg set TS_AUTHKEY` for auth key storage
- `/charly-selkies:selkies-labwc` — Full deployment example with Tailscale exit node
- `/charly-selkies:chrome` — Proxy deployment pattern (Tailscale exit node + HTTP_PROXY) and NO_PROXY auto-enrichment
- `/charly-automation:enc` — Encrypted volumes in pod deployments

## Source

`candy/plugin-deploy-pod/sidecar_resolve.go` (types, merge, resolution — `resolvePodSidecars` / `resolvedSidecarFromSpec`; the types are `spec.ResolvedSidecar` / `deploykit.ResolvedSidecar`; the former `charly/sidecar.go` is DELETED, K-wave 2), `candy/plugin-deploy-pod/sidecar_embedded.go` (the embedded DEFAULT sidecar template library — the former `charly/sidecar.go`'s `embeddedSidecarBodies`, now this plugin's own `//go:embed sidecar_templates.json`, served to `resolvePodSidecars` + the `charly config --list-sidecars` leaf), `spec/spec/config.go` (`Config.Sidecar`, the project's authored sidecar-template library as opaque bodies), `candy/plugin-deploy-pod/config_setup.go` / `config_write.go` (the `charly config --sidecar <name>` attachment + pod + sidecar + app quadlet emission — the former `charly/quadlet_pod.go` is DELETED, K-wave 2).
