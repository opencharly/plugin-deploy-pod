package deploypod

// config_secret_env.go — the CONFIG-TIME half of secret delivery (D-SEC-1), and the box scoping that
// makes it safe.
//
// THE PROPERTY, and it is D-SEC-1's `state` limb rather than its IR limb: a value that reaches a
// container through the container's `Env:` is WRITTEN DOWN — the unit file is state, and the config
// writer persists `Environment=NAME=value` into it. DESIGN.md:2511 forbids exactly that. So this file
// no longer carries a value anywhere:
//
//   - It ENSURES the deploy box chain's required secret EXISTS in the credential store (minting one
//     when a `secret_require:` is missing everywhere — the documented auto-generation), and returns
//     the NAMES it ensured.
//   - DELIVERY is the podman-secret reference the image's own label already supplies:
//     `CollectCandySecretAccepts` reads `meta.SecretRequire`, `ProvisionPodmanSecrets` creates the
//     podman secret, and the unit carries `Secret=<name>,type=env,target=<VAR>`. Podman resolves the
//     value at REALIZE, so the unit holds a NAME and the process still sees the variable.
//
// The ensure runs BEFORE provisioning (see the call site) so a value minted on this very first
// config is the one provisioned.
//
// THE HAZARD THIS FILE EXISTS TO NOT MINT: `EnsureCandySecret` (the shared coordinate rule) GENERATES
// AND PERSISTS a 32-byte token for a required dep that is missing everywhere. Calling it for every
// candy in the project would therefore mint credentials an operator never asked for, silently, on
// `charly config`. So the resolve is scoped to THE BOX THIS DEPLOY RUNS — its own `candy:` list plus
// every base box's, walked the way `deploykit.BoxCandyChain` walks it (`Base`/`From` to the roots,
// first-occurrence-wins).

import (
	"encoding/json"

	"github.com/opencharly/sdk/deploykit"
	"github.com/opencharly/spec/spec"
)

// boxCandyChain returns the candy names the box `base` composes across its FULL base chain, in
// first-occurrence order. `uf.Box` is the WIRE form (name → raw JSON), so each level is opened
// through `spec.BoxConfigFrom`, the typed door — reading the map directly is the mistake that made
// every namespace-qualified box resolve to nothing.
//
// The walk mirrors `deploykit.BoxCandyChain`'s traversal (`WalkBaseChain`: box → base → base's base)
// but cannot call it: that function needs a `*spec.Config`, which the config path does not hold, and
// a hand-built one would be a second parser. What it deliberately does NOT do is `ResolveCandyOrder`'s
// transitive expansion of `candy:` composition — this returns the AUTHORED names, and a name that is
// absent from `uf.Candy` is skipped by the caller rather than guessed at.
func boxCandyChain(uf *spec.UnifiedFile, base string) []string {
	var out []string
	seen := map[string]bool{}
	visited := map[string]bool{}
	for base != "" {
		bc, ok := spec.BoxConfigFrom(uf.Box, base)
		if !ok {
			break
		}
		for _, name := range bc.Candy {
			if !seen[name] {
				seen[name] = true
				out = append(out, name)
			}
		}
		next := bc.Base
		if next == "" {
			next = bc.From
		}
		// A visited set on BOX NAMES, not on candy names. Tracking candies cannot detect a cycle:
		// `A -> B -> A` revisits a box whose candies are already in `out`, so the candy-level
		// `seen` never stops it and `charly config` hangs. `if next == base` catches only the
		// self-loop `A -> A`. A malformed manifest must fail loudly elsewhere; this walk must
		// TERMINATE regardless, because a config command that hangs is worse than one that
		// reports a bad chain.
		if visited[base] {
			break
		}
		visited[base] = true
		base = next
	}
	return out
}

// ensureBoxChainSecrets ensures the secrets of the NAMED candies only, and returns the NAMES it
// ensured — never a value (D-SEC-1: a value must not enter state, and the caller can only put what
// it is handed into state). `raw` is `uf.Candy` (name -> authored body).
//
// The body is decoded into spec.Candy -- the TYPED model -- not into a local struct with
// json:"secret_require" tags. A hand-written mirror of two schema fields is a second copy of the
// contract that drifts the moment the schema grows a third (SDD: Go is generated, never
// hand-transcribed). spec.Candy already carries SecretRequire and SecretAccept, both
// []spec.EnvDependency.
//
// A name in `chain` that has no authored body is skipped: the box chain can name a candy the
// unified file does not carry inline, and inventing a body for it is how a mint would happen.
//
// The resolution itself is `deploykit.EnsureCandySecret`, the SHARED function `ResolveSecretForCandy`
// calls per dep (it owns the `<service>/<key>` coordinate rule -- the predicate opencharly/sdk#52 and
// opencharly/pod-openwebui#19 both drifted from). This file decides WHICH candies, never HOW.
func ensureBoxChainSecrets(raw map[string]json.RawMessage, chain []string, cred deploykit.CredentialAccess) []string {
	var out []string
	for _, name := range chain {
		body, ok := raw[name]
		if !ok {
			continue
		}
		var deps spec.Candy // the TYPED model -- never a hand-written mirror of its fields
		if err := json.Unmarshal(body, &deps); err != nil {
			continue
		}
		for _, dep := range deps.SecretRequire {
			if v, _ := deploykit.EnsureCandySecret(dep, true, cred); v != "" {
				out = append(out, dep.Name)
			}
		}
		for _, dep := range deps.SecretAccept {
			if v, _ := deploykit.EnsureCandySecret(dep, false, cred); v != "" {
				out = append(out, dep.Name)
			}
		}
	}
	return out
}

// deployBoxBase answers ONE question: which box's candy chain belongs to this deploy? The
// deploy's own box first -- `box` is definitionally a box name, and the caller resolved the
// project with it. `node.Image` is the image the deploy runs, and for a BOX-based deploy it
// names a box (check-pod-overlay's `image: check-k8s-deploy-app` is a box), but for a registry
// deploy it is a ref like `quay.io/fedora/fedora:43` that names no box at all.
//
// Preferring Image made a registry deploy's chain EMPTY instead of falling back to its box --
// invisible in a box-based bed, which is why it needed its own test rather than a bed.
func deployBoxBase(uf *spec.UnifiedFile, dc *spec.DeployConfig, box, instance string) string {
	if _, isBox := spec.BoxConfigFrom(uf.Box, box); isBox {
		return box
	}
	if dc == nil {
		return box
	}
	node, found := dc.Deploy[spec.DeployKey(box, instance)]
	if !found || node.Image == "" {
		return box
	}
	if _, imageIsBox := spec.BoxConfigFrom(uf.Box, node.Image); imageIsBox {
		return node.Image
	}
	return box
}

// EnsureBoxChainSecrets is THE ensure for a pod's config: it makes the deploy's box-chain required
// secrets exist in the credential store and returns the NAMES it ensured. It takes NO env slice and
// returns no value, because the container's `Env:` is state (D-SEC-1) and the reference the unit
// carries is the podman secret's NAME. Exported so a test can drive the box chain and the scope
// guard; the values themselves never leave the store.
func EnsureBoxChainSecrets(uf *spec.UnifiedFile, dc *spec.DeployConfig, box, instance string, cred deploykit.CredentialAccess) []string {
	if uf == nil {
		return nil
	}
	base := deployBoxBase(uf, dc, box, instance)
	var addCandy []string
	if dc != nil {
		if node, found := dc.Deploy[spec.DeployKey(box, instance)]; found {
			addCandy = node.AddCandy
		}
	}
	return ensureBoxChainSecrets(uf.Candy, DeployCandyChain(uf, base, addCandy), cred)
}

// DeployCandyChain is D-CAP-6's UNION: "The needs and uses of a deployment are the union of those of
// the DEPLOYMENT, its box and ALL THEIR CANDIES" (DESIGN.md:695). An `add_candy:` candy IS one of the
// deployment's candies -- it runs in the container and can author `secret_require:` -- so a chain that
// walked only `uf.Box` would under-resolve a deployment using the overlay idiom this project's own pod
// bed (`check-pod-overlay`) actually uses.
//
// ORDER: the box chain first, then the deploy's `add_candy:` list, first-occurrence-wins. The box is the
// image the overlay rides ON, so where a candy appears in both, the box's reading of it wins -- the
// overlay names a candy, it does not re-define one.
func DeployCandyChain(uf *spec.UnifiedFile, base string, addCandy []string) []string {
	out := boxCandyChain(uf, base)
	seen := make(map[string]bool, len(out))
	for _, name := range out {
		seen[name] = true
	}
	for _, name := range addCandy {
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	return out
}
