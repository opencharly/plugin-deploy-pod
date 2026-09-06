package deploypod

// member_tree_accept_test.go — the Cutover C consumer proof for THIS plugin's config-parse
// surface. The plugin runs the whole project loader PLUGIN-SIDE (loaderkit.LoadUnifiedViaExecutor
// + GateSchemaVersion + ParseDoc with this binary's embedded spec pin), so its embedded
// spec.SchemaVersion IS the schema cap a migrated project hits. The pre-wave failure was:
// "config schema 2026.249.2125 is newer than this charly supports" — the plugin rejected a
// migrated (group-unrolled) tree. This test pins the NEW world on the exact converted
// distro-fedora shape (primary substrate + deploy-level sibling, ZERO group: nodes):
//
//  1. GateSchemaVersion accepts the migrated stamp 2026.249.2125 (the embedded spec pin
//     carries HEAD 2026.249.2125 — spec v0.2026249.2129, the version-bump tag on top of
//     the 2106 group-kind removal).
//  2. ParseDoc/BuildFleetNode parse + fold that shape into the position-derived member
//     tree (sdk v0.2026249.2114): DeployLevelMembers/InSubstrateMembers/MemberByName/
//     HasMembers — the settled contract replacing the deleted dual-map surface.
import (
	"testing"

	"github.com/opencharly/sdk/loaderkit"
	calverpkg "github.com/opencharly/spec/calver"
	"github.com/opencharly/spec/spec"
	"gopkg.in/yaml.v3"
)

// docNode parses a raw YAML document string into the *yaml.Node ParseDoc consumes
// (mirrors loaderkit's own test helper).
func docNode(t *testing.T, s string) *yaml.Node {
	t.Helper()
	var n yaml.Node
	if err := yaml.Unmarshal([]byte(s), &n); err != nil {
		t.Fatalf("yaml: %v", err)
	}
	return &n
}

// convertedFedoraDoc is the post-migrate spelling of a fedora-flavored cross-deployment
// deploy: ONE entity whose PRIMARY substrate is fedora, carrying an IN-BODY sidecar member
// and a DEPLOY-LEVEL sibling (fedora-tools) — the unrollGroupDeploy post-migrate spelling
// (first former group member → primary substrate, remaining members → deploy-level
// siblings). No 'group:' kind anywhere.
const convertedFedoraDoc = `fedora-main:
  fedora:
    from: fedora-base
    sidecar:
      pod:
        image: ghcr.io/opencharly/toolbox:latest
  fedora-tools:
    fedora:
      from: fedora-tools-base
`

// convertedFedoraThreaded recognizes the fedora substrate the doc authors, mirroring the
// vmBedThreaded pattern from loaderkit's own member-tree tests.
var convertedFedoraThreaded = spec.Threaded{
	Kinds:            map[string]bool{"pod": true, "fedora": true},
	DeploySubstrates: map[string]bool{"pod": true, "fedora": true},
	StructuralKinds:  map[string]bool{"pod": true, "fedora": true},
	Primaries:        map[string]string{},
	DeployTraits: map[string]*spec.DeployTraits{
		"fedora": {Venue: "ssh"},
		"pod":    {Venue: "container", ImageBacked: true},
	},
}

func TestMigratedTreeStampAcceptedByEmbeddedParser(t *testing.T) {
	head := calverpkg.MustCalVer(spec.SchemaVersion)
	stamp := calverpkg.MustCalVer("2026.249.2125")
	if head.Less(stamp) {
		t.Fatalf("embedded spec cap %s rejects the migrated stamp 2026.249.2125 — the pre-wave failure is NOT fixed", spec.SchemaVersion)
	}
	if err := loaderkit.GateSchemaVersion("charly.yml", "2026.249.2125"); err != nil {
		t.Fatalf("GateSchemaVersion rejected the migrated stamp: %v", err)
	}
}

func TestMigratedMemberTreeShapeParsesAndFolds(t *testing.T) {
	_, pp, err := loaderkit.ParseDoc(docNode(t, convertedFedoraDoc), convertedFedoraThreaded)
	if err != nil {
		t.Fatalf("the embedded parser REJECTED the converted member-tree shape: %v", err)
	}
	if len(pp.Nodes) != 1 {
		t.Fatalf("nodes = %d, want 1 (fedora-main)", len(pp.Nodes))
	}
	dn, err := loaderkit.BuildFleetNode(pp.Nodes[0], convertedFedoraThreaded)
	if err != nil {
		t.Fatalf("BuildFleetNode: %v", err)
	}
	if dn.Target != "fedora" {
		t.Fatalf("primary substrate target = %q, want fedora", dn.Target)
	}
	// The unrollGroupDeploy contract: first member → primary substrate (in-body sidecar),
	// remaining members → deploy-level siblings.
	if !dn.HasMembers() {
		t.Fatalf("HasMembers() = false — the member tree did not fold")
	}
	inSubstrate := dn.InSubstrateMembers()
	if len(inSubstrate) != 1 || inSubstrate[0].Node == nil || inSubstrate[0].Node.Target != "pod" {
		t.Fatalf("InSubstrateMembers() = %+v, want the pod sidecar", inSubstrate)
	}
	deployLevel := dn.DeployLevelMembers()
	if len(deployLevel) != 1 || deployLevel[0].Node == nil || deployLevel[0].Node.Target != "fedora" {
		t.Fatalf("DeployLevelMembers() = %+v, want the fedora-tools sibling", deployLevel)
	}
	if m := dn.MemberByName("fedora-tools"); m == nil || !m.Alongside() {
		t.Fatalf("MemberByName(fedora-tools) = %+v, want a deploy-level (alongside) member", m)
	}
	if m := dn.MemberByName("sidecar"); m == nil || !m.InSubstrate() {
		t.Fatalf("MemberByName(sidecar) = %+v, want an in-substrate member", m)
	}
	// The deleted dual-map surface stays deleted: a member-bearing deploy always carries
	// its primary substrate target — the group shape never leaked back.
	if dn.Target == "" {
		t.Fatalf("member-bearing deploy lost its primary substrate target — the group shape leaked back")
	}
}
