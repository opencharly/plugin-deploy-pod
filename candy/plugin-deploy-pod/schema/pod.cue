// plugin-deploy-pod's OWN self-contained CUE schema — the SINGLE SOURCE for this
// plugin's declaration surface. There is NO schema-less plugin: every plugin
// ships a non-empty, self-contained schema, served over Describe (the SDK splices
// `base ++ plugin` at the load gate), and this one DOCUMENTS the deploy
// capability.
//
// SELF-CONTAINED: it references NO base def, so it compiles STANDALONE — the exact
// property `cue exp gengotypes` needs to generate Go params, AND the property that
// lets the SDK compile it serve-side.
#DeployPodPlugin: {
	// The deploy substrate word this plugin serves.
	deploy: "pod"

	// The substrate owns a venue lifecycle (prepare-venue/start/stop/…).
	lifecycle: true

	// What the plugin does, in one line (the public-docs surface).
	contract: string & !=""
}
