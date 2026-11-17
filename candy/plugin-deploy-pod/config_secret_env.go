package deploypod

// config_secret_env.go — the CONFIG-TIME half of secret delivery (D-SEC-1), and the box scoping that
// makes it safe.
//
// THE PROPERTY: `Op.Env` is baked into the rendered script at PLAN time; a container's `Env:` is
// written at CONFIG time, host-side. The same secret can reach the process either way, but only one
// of those paths puts it in the IR. This file is the config-time path: the value goes from the
// credential store into `config_setup.go`'s `envVars`, and no plan is in scope.
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
		if next == base {
			break
		}
		base = next
	}
	return out
}

// candySecretDeps is the only part of a candy body this file reads: its two authored secret lists.
type candySecretDeps struct {
	SecretRequire []spec.EnvDependency `json:"secret_require"`
	SecretAccept  []spec.EnvDependency `json:"secret_accept"`
}

// scopedCandySecrets resolves the secrets of the NAMED candies only, and returns them as the
// environment pairs the container is configured with. `raw` is `uf.Candy` (name → authored body).
//
// A name in `chain` that has no authored body is skipped: the box chain can name a candy the unified
// file does not carry inline, and inventing a body for it is how a mint would happen.
//
// The resolution itself is `deploykit.EnsureCandySecret`, the SHARED function `ResolveSecretForCandy`
// calls per dep (it owns the `<service>/<key>` coordinate rule — the predicate opencharly/sdk#526 and
// opencharly/pod-openwebui#19 both drifted from). This file decides WHICH candies, never HOW.
func scopedCandySecrets(raw map[string]json.RawMessage, chain []string, cred deploykit.CredentialAccess) map[string]string {
	out := map[string]string{}
	for _, name := range chain {
		body, ok := raw[name]
		if !ok {
			continue
		}
		var deps candySecretDeps
		if err := json.Unmarshal(body, &deps); err != nil {
			continue
		}
		for _, dep := range deps.SecretRequire {
			if v, _ := deploykit.EnsureCandySecret(dep, true, cred); v != "" {
				out[dep.Name] = v
			}
		}
		for _, dep := range deps.SecretAccept {
			if v, _ := deploykit.EnsureCandySecret(dep, false, cred); v != "" {
				out[dep.Name] = v
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
