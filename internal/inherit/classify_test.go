package inherit

import (
	"testing"

	"github.com/rognerud/dbt-ditto/internal/dbt"
)

// at places a node in a warehouse location. The test helpers build nodes
// without one, and classification is entirely about the relation.
func at(n *dbt.Node, database, schema string) *dbt.Node {
	n.Database, n.Schema = database, schema
	return n
}

// classify runs ClassifySources and indexes both halves by unique_id.
func classify(projects ...*dbt.Project) (map[string]bool, map[string]ShadowedSource) {
	external, shadowed := BuildGraph(projects).ClassifySources()
	ext := make(map[string]bool, len(external))
	for _, n := range external {
		ext[n.UniqueID] = true
	}
	sh := make(map[string]ShadowedSource, len(shadowed))
	for _, s := range shadowed {
		sh[s.Source.UniqueID] = s
	}
	return ext, sh
}

func TestSourceNoProjectBuildsIsExternal(t *testing.T) {
	p := project("plat")
	attach(p, at(source("plat", "crm", "raw_customers"), "warehouse", "raw"))

	ext, sh := classify(p)
	if !ext["source.plat.crm.raw_customers"] {
		t.Fatalf("source with nothing building it should be external, got %v", ext)
	}
	if len(sh) != 0 {
		t.Fatalf("nothing builds it, so nothing shadows it: %v", sh)
	}
}

func TestSourceBuiltByAModelIsShadowedAndAMistake(t *testing.T) {
	// The pre-loom pattern: rather than depending on the upstream project, the
	// downstream one declares its output as a source.
	up := project("plat")
	attach(up, at(node("plat", "dim_customers", nil), "warehouse", "analytics"))

	down := project("mart")
	shadow := at(source("mart", "platform", "dim_customers"), "warehouse", "analytics")
	attach(down, shadow)

	ext, sh := classify(up, down)
	if ext["source.mart.platform.dim_customers"] {
		t.Fatal("a source a loaded project builds is not external and must not reach a provider")
	}
	s, ok := sh["source.mart.platform.dim_customers"]
	if !ok {
		t.Fatalf("expected the source to be reported as shadowed, got %v", sh)
	}
	if s.By.UniqueID != "model.plat.dim_customers" {
		t.Fatalf("shadowed by %s, want model.plat.dim_customers", s.By.UniqueID)
	}
	if !s.Mistake() {
		t.Fatal("a source standing in for a model is the case worth warning about")
	}
}

func TestSourceBackedByASeedIsShadowedButNotAMistake(t *testing.T) {
	// Seeds are how a project stands up fake raw data; the source declaration is
	// what the rest of the project reads it through. Both halves are meant to
	// exist, so this must not produce a warning — but it is still not external,
	// because the documentation is already in dbt.
	p := project("plat")
	attach(p,
		at(seed("plat", "raw_orders"), "warehouse", "raw"),
		at(source("plat", "crm", "raw_orders"), "warehouse", "raw"),
	)

	ext, sh := classify(p)
	if ext["source.plat.crm.raw_orders"] {
		t.Fatal("a seed-backed source needs no provider: dbt already knows the columns")
	}
	s, ok := sh["source.plat.crm.raw_orders"]
	if !ok {
		t.Fatalf("expected the source to be reported as shadowed, got %v", sh)
	}
	if s.Mistake() {
		t.Fatal("a seed-backed source is a deliberate pattern, not something to warn about")
	}
}

func TestSameRelationInAnotherDatabaseDoesNotShadow(t *testing.T) {
	p := project("plat")
	attach(p,
		at(node("plat", "customers", nil), "prod", "raw"),
		at(source("plat", "crm", "customers"), "staging", "raw"),
	)

	ext, sh := classify(p)
	if !ext["source.plat.crm.customers"] {
		t.Fatalf("another database is another table, so the source is still external: %v", ext)
	}
	if len(sh) != 0 {
		t.Fatalf("no shadow across databases, got %v", sh)
	}
}

func TestEphemeralModelDoesNotShadow(t *testing.T) {
	// An ephemeral model is inlined as a CTE and never materializes, so a source
	// cannot be pointing at it and a matching name is coincidence.
	p := project("plat")
	eph := at(node("plat", "raw_events", nil), "warehouse", "raw")
	eph.Config = map[string]any{"materialized": "ephemeral"}
	attach(p, eph, at(source("plat", "events", "raw_events"), "warehouse", "raw"))

	ext, sh := classify(p)
	if !ext["source.plat.events.raw_events"] {
		t.Fatalf("an ephemeral model builds nothing, so the source is external: %v", ext)
	}
	if len(sh) != 0 {
		t.Fatalf("expected no shadow, got %v", sh)
	}
}

func TestIdentifierNotNameDecidesTheRelation(t *testing.T) {
	// A source's `identifier:` is the table it really points at; its name is
	// just what dbt calls it. Matching on the name would miss the shadow.
	up := project("plat")
	attach(up, at(node("plat", "dim_customers", nil), "warehouse", "analytics"))

	down := project("mart")
	s := at(source("mart", "platform", "customers"), "warehouse", "analytics")
	s.Identifier = "dim_customers"
	attach(down, s)

	_, sh := classify(up, down)
	if _, ok := sh["source.mart.platform.customers"]; !ok {
		t.Fatalf("identifier decides the relation, got %v", sh)
	}
}
