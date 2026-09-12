package inherit

import (
	"reflect"
	"strings"
	"testing"

	"github.com/rognerud/dbt-ditto/internal/config"
	"github.com/rognerud/dbt-ditto/internal/dbt"
)

// --- fixture helpers -------------------------------------------------------

// col builds a manifest column.
func col(name, desc string, opts ...func(*dbt.Column)) *dbt.Column {
	c := &dbt.Column{Name: name, Description: desc}
	for _, o := range opts {
		o(c)
	}
	return c
}

// withMeta attaches meta in the given key order.
func withMeta(kv ...any) func(*dbt.Column) {
	return func(c *dbt.Column) {
		m := dbt.NewOrderedMap()
		for i := 0; i+1 < len(kv); i += 2 {
			m.Set(kv[i].(string), kv[i+1])
		}
		c.Meta = m
	}
}

// withConfigMeta attaches meta under the dbt >= 1.9.6 `config:` block.
func withConfigMeta(kv ...any) func(*dbt.Column) {
	return func(c *dbt.Column) {
		m := dbt.NewOrderedMap()
		for i := 0; i+1 < len(kv); i += 2 {
			m.Set(kv[i].(string), kv[i+1])
		}
		if c.Config == nil {
			c.Config = &dbt.ColumnConfig{}
		}
		c.Config.Meta = m
	}
}

func withTags(tags ...string) func(*dbt.Column) {
	return func(c *dbt.Column) { c.Tags = tags }
}

// node builds a model node.
func node(pkg, name string, deps []string, cols ...*dbt.Column) *dbt.Node {
	n := &dbt.Node{
		UniqueID:     "model." + pkg + "." + name,
		Name:         name,
		ResourceType: "model",
		PackageName:  pkg,
		Columns:      map[string]*dbt.Column{},
	}
	for _, c := range cols {
		n.Columns[c.Name] = c
	}
	for _, d := range deps {
		if strings.Contains(d, ".") {
			n.DependsOn.Nodes = append(n.DependsOn.Nodes, d)
		} else {
			n.DependsOn.Nodes = append(n.DependsOn.Nodes, "model."+pkg+"."+d)
		}
	}
	return n
}

// seed builds a seed node, which sorts before models on unique_id and so is a
// useful way to exercise the tie-break within a generation.
func seed(pkg, name string, cols ...*dbt.Column) *dbt.Node {
	n := node(pkg, name, nil, cols...)
	n.UniqueID = "seed." + pkg + "." + name
	n.ResourceType = "seed"
	return n
}

// project wraps nodes into a project and returns it.
func project(name string, nodes ...*dbt.Node) *dbt.Project {
	p := &dbt.Project{Name: name, Writable: true, Manifest: &dbt.Manifest{
		Nodes:   map[string]*dbt.Node{},
		Sources: map[string]*dbt.Node{},
	}}
	for i, n := range nodes {
		n.Project = p
		n.Manifest = p.Manifest
		n.Order = i
		p.Manifest.Nodes[n.UniqueID] = n
	}
	return p
}

// catalogFor gives a project a catalog saying the node has exactly these columns, in
// this order.
func catalogFor(p *dbt.Project, n *dbt.Node, columns ...string) {
	if p.Catalog == nil {
		p.Catalog = &dbt.Catalog{
			Nodes:   map[string]*dbt.CatalogNode{},
			Sources: map[string]*dbt.CatalogNode{},
		}
	}
	entry := &dbt.CatalogNode{Columns: map[string]dbt.CatalogColumn{}}
	for i, c := range columns {
		entry.Columns[c] = dbt.CatalogColumn{Name: c, Index: i, Type: "VARCHAR"}
	}
	p.Catalog.Nodes[n.UniqueID] = entry
}

// resolverFor builds a resolver over the given projects with default settings,
// optionally tweaked.
func resolverFor(t *testing.T, tweak func(*config.Config), projects ...*dbt.Project) *Resolver {
	t.Helper()
	c := &config.Config{}
	if tweak != nil {
		tweak(c)
	}
	return &Resolver{Graph: BuildGraph(projects), Cfg: c.Resolve()}
}

// columnDoc finds the resolved column by name.
func columnDoc(t *testing.T, doc *NodeDoc, name string) ColumnDoc {
	t.Helper()
	for _, c := range doc.Columns {
		if strings.EqualFold(c.Name, name) {
			return c
		}
	}
	t.Fatalf("column %q not resolved; got %v", name, columnNames(doc))
	return ColumnDoc{}
}

func columnNames(doc *NodeDoc) []string {
	out := make([]string, 0, len(doc.Columns))
	for _, c := range doc.Columns {
		out = append(out, c.Name)
	}
	return out
}

func metaMap(entries []MetaEntry) map[string]any {
	out := map[string]any{}
	for _, e := range entries {
		out[e.Key] = e.Value
	}
	return out
}

func metaOrder(entries []MetaEntry) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Key)
	}
	return out
}

// --- description inheritance ----------------------------------------------

func TestDescriptionInheritsWhenColumnIsEmpty(t *testing.T) {
	up := seed("p", "raw", col("id", "The identifier."))
	down := node("p", "stg", []string{"seed.p.raw"}, col("id", ""))
	r := resolverFor(t, nil, project("p", up, down))

	doc := r.Resolve(down, Existing{})
	got := columnDoc(t, doc, "id")

	if !got.SetDescription || got.Description != "The identifier." {
		t.Fatalf("description = %q (set=%v), want the upstream text", got.Description, got.SetDescription)
	}
	if got.Progenitor != "seed.p.raw" {
		t.Errorf("progenitor = %q, want seed.p.raw", got.Progenitor)
	}
}

func TestLocalDescriptionAlwaysWins(t *testing.T) {
	up := seed("p", "raw", col("id", "Upstream text."))
	down := node("p", "stg", []string{"seed.p.raw"}, col("id", "Local text."))
	r := resolverFor(t, nil, project("p", up, down))

	got := columnDoc(t, r.Resolve(down, Existing{
		Columns: []ExistingColumn{{Name: "id", Description: "Local text."}},
	}), "id")

	if got.SetDescription {
		t.Fatalf("rewrote a locally documented column to %q", got.Description)
	}
}

// A local description that happens to be one of dbt-osmosis' placeholder
// strings is still a local description: only an empty one inherits.
func TestLocalPlaceholderDescriptionIsNotReplaced(t *testing.T) {
	up := seed("p", "raw", col("id", "Upstream text."))
	down := node("p", "stg", []string{"seed.p.raw"}, col("id", "Not documented"))
	r := resolverFor(t, nil, project("p", up, down))

	got := columnDoc(t, r.Resolve(down, Existing{
		Columns: []ExistingColumn{{Name: "id", Description: "Not documented"}},
	}), "id")

	if got.SetDescription {
		t.Fatalf("replaced a local placeholder with %q", got.Description)
	}
}

// An *upstream* placeholder, by contrast, is treated as no documentation and
// the search continues to the next generation.
func TestUpstreamPlaceholderIsSkipped(t *testing.T) {
	root := seed("p", "raw", col("id", "The real text."))
	mid := node("p", "mid", []string{"seed.p.raw"}, col("id", "Undefined"))
	down := node("p", "stg", []string{"mid"}, col("id", ""))
	r := resolverFor(t, nil, project("p", root, mid, down))

	got := columnDoc(t, r.Resolve(down, Existing{}), "id")
	if got.Description != "The real text." {
		t.Fatalf("description = %q, want the text from beyond the placeholder", got.Description)
	}
}

func TestForceOverwritesLocalDescription(t *testing.T) {
	up := seed("p", "raw", col("id", "Upstream text."))
	down := node("p", "stg", []string{"seed.p.raw"}, col("id", "Local text."))
	force := true
	r := resolverFor(t, func(c *config.Config) { c.Inheritance.Force = &force }, project("p", up, down))

	got := columnDoc(t, r.Resolve(down, Existing{
		Columns: []ExistingColumn{{Name: "id", Description: "Local text."}},
	}), "id")

	if !got.SetDescription || got.Description != "Upstream text." {
		t.Fatalf("description = %q, want the upstream text under force", got.Description)
	}
}

// --- conflicting upstreams -------------------------------------------------

// Two parents in the same generation both document the column.
func TestConflictWithinAGenerationIsSettledByUniqueID(t *testing.T) {
	a := node("p", "alpha", nil, col("id", "From alpha.", withTags("alpha")))
	b := node("p", "beta", nil, col("id", "From beta.", withTags("beta")))
	down := node("p", "stg", []string{"alpha", "beta"}, col("id", ""))
	r := resolverFor(t, nil, project("p", a, b, down))

	got := columnDoc(t, r.Resolve(down, Existing{}), "id")
	if got.Description != "From alpha." {
		t.Errorf("description = %q, want From alpha. (model.p.alpha sorts first)", got.Description)
	}
	if !reflect.DeepEqual(got.Tags, []string{"alpha"}) {
		t.Errorf("tags = %v, want only alpha: the losing parent must be skipped whole", got.Tags)
	}
}

// A seed sorts before a model on unique_id only if its name does; the tie-break
// is the full unique_id, so `model.p.zeta` beats `seed.p.alpha`.
func TestGenerationTieBreakUsesTheWholeUniqueID(t *testing.T) {
	s := seed("p", "alpha", col("id", "From the seed."))
	m := node("p", "zeta", nil, col("id", "From the model."))
	down := node("p", "stg", []string{"seed.p.alpha", "zeta"}, col("id", ""))
	r := resolverFor(t, nil, project("p", s, m, down))

	got := columnDoc(t, r.Resolve(down, Existing{}), "id")
	if got.Description != "From the model." {
		t.Errorf("description = %q, want the model's: model.p.zeta < seed.p.alpha", got.Description)
	}
}

// The nearer generation wins over the further one regardless of unique_id.
func TestNearerGenerationWins(t *testing.T) {
	root := seed("p", "raw", col("id", "From the root."))
	mid := node("p", "mid", []string{"seed.p.raw"}, col("id", "From the middle."))
	down := node("p", "stg", []string{"mid"}, col("id", ""))
	r := resolverFor(t, nil, project("p", root, mid, down))

	got := columnDoc(t, r.Resolve(down, Existing{}), "id")
	if got.Description != "From the middle." {
		t.Errorf("description = %q, want the nearer ancestor's", got.Description)
	}
}

// An undocumented model still shadows its own ancestors for the columns it carries: it
// claims the column for its generation and contributes nothing.
func TestUndocumentedAncestorStillClaimsItsGeneration(t *testing.T) {
	root := seed("p", "raw", col("id", "From the root.", withTags("pk")))
	// model.p.blank sorts before seed.p.raw and has the column but no docs.
	blank := node("p", "blank", nil, col("id", ""))
	down := node("p", "stg", []string{"blank", "seed.p.raw"}, col("id", ""))
	r := resolverFor(t, nil, project("p", root, blank, down))

	got := columnDoc(t, r.Resolve(down, Existing{}), "id")
	if got.SetDescription {
		t.Errorf("description = %q, want none: the blank parent claimed the column", got.Description)
	}
	if len(got.Tags) != 0 {
		t.Errorf("tags = %v, want none", got.Tags)
	}
}

// Documentation still travels through an intermediate model that has the column
// but no documentation of its own, as long as nothing else in that generation
// claims it first.
func TestDocumentationPassesThroughAnEmptyMiddleModel(t *testing.T) {
	root := seed("p", "raw", col("id", "From the root.", withTags("pk")))
	mid := node("p", "mid", []string{"seed.p.raw"}, col("id", ""))
	down := node("p", "stg", []string{"mid"}, col("id", ""))
	r := resolverFor(t, nil, project("p", root, mid, down))

	got := columnDoc(t, r.Resolve(down, Existing{}), "id")
	if got.Description != "From the root." {
		t.Errorf("description = %q, want the root's", got.Description)
	}
	if !reflect.DeepEqual(got.Tags, []string{"pk"}) {
		t.Errorf("tags = %v, want [pk]", got.Tags)
	}
}

// --- meta and tags ---------------------------------------------------------

// Upstream meta overwrites the local value for the same key, local-only keys
// survive, and the local key order is preserved.
func TestUpstreamMetaOverridesLocalAndKeepsOrder(t *testing.T) {
	up := seed("p", "raw", col("id", "Upstream.", withMeta("owner", "platform", "pii", false)))
	down := node("p", "stg", []string{"seed.p.raw"},
		col("id", "Local.", withMeta("owner", "mart", "pii", true, "sensitivity", "low")))
	r := resolverFor(t, nil, project("p", up, down))

	got := columnDoc(t, r.Resolve(down, Existing{}), "id")
	want := map[string]any{"owner": "platform", "pii": false, "sensitivity": "low"}
	if !reflect.DeepEqual(metaMap(got.Meta), want) {
		t.Errorf("meta = %v, want %v", metaMap(got.Meta), want)
	}
	if order := metaOrder(got.Meta); !reflect.DeepEqual(order, []string{"owner", "pii", "sensitivity"}) {
		t.Errorf("meta key order = %v, want the local order preserved", order)
	}
}

func TestMetaFromTheConfigBlockIsRead(t *testing.T) {
	up := seed("p", "raw", col("id", "Upstream.", withConfigMeta("owner", "platform")))
	down := node("p", "stg", []string{"seed.p.raw"}, col("id", ""))
	r := resolverFor(t, nil, project("p", up, down))

	got := columnDoc(t, r.Resolve(down, Existing{}), "id")
	if metaMap(got.Meta)["owner"] != "platform" {
		t.Errorf("meta = %v, want owner inherited from config.meta", metaMap(got.Meta))
	}
}

func TestSkippedMetaKeysAreNotInherited(t *testing.T) {
	up := seed("p", "raw", col("id", "Upstream.", withMeta("owner", "platform", "pii", false)))
	down := node("p", "stg", []string{"seed.p.raw"}, col("id", ""))
	r := resolverFor(t, func(c *config.Config) {
		c.Inheritance.SkipMetaKeys = []string{"owner"}
	}, project("p", up, down))

	got := columnDoc(t, r.Resolve(down, Existing{}), "id")
	if _, ok := metaMap(got.Meta)["owner"]; ok {
		t.Errorf("meta = %v, want owner skipped", metaMap(got.Meta))
	}
	if metaMap(got.Meta)["pii"] != false {
		t.Errorf("meta = %v, want the other keys still inherited", metaMap(got.Meta))
	}
}

// Tags accumulate across generations, local first, then furthest to nearest.
func TestTagsAccumulateAcrossGenerations(t *testing.T) {
	root := seed("p", "raw", col("id", "Root.", withTags("root")))
	mid := node("p", "mid", []string{"seed.p.raw"}, col("id", "Mid.", withTags("mid")))
	down := node("p", "stg", []string{"mid"}, col("id", "", withTags("local")))
	r := resolverFor(t, nil, project("p", root, mid, down))

	got := columnDoc(t, r.Resolve(down, Existing{}), "id")
	want := []string{"local", "root", "mid"}
	if !reflect.DeepEqual(got.Tags, want) {
		t.Errorf("tags = %v, want %v", got.Tags, want)
	}
}

func TestDisablingMetaAndTagInheritance(t *testing.T) {
	up := seed("p", "raw", col("id", "Upstream.", withMeta("owner", "platform"), withTags("pk")))
	down := node("p", "stg", []string{"seed.p.raw"}, col("id", ""))
	off := false
	r := resolverFor(t, func(c *config.Config) {
		c.Inheritance.Meta = &off
		c.Inheritance.Tags = &off
		// The progenitor annotation is meta too, and it is on by default.
		c.Inheritance.Progenitor = &off
	}, project("p", up, down))

	got := columnDoc(t, r.Resolve(down, Existing{}), "id")
	if len(got.Meta) != 0 || len(got.Tags) != 0 {
		t.Errorf("meta = %v, tags = %v, want both empty", got.Meta, got.Tags)
	}
	if got.Description != "Upstream." {
		t.Errorf("description = %q, want descriptions still inherited", got.Description)
	}
}

// --- case handling ---------------------------------------------------------

func TestColumnsMatchAcrossCase(t *testing.T) {
	up := seed("p", "raw", col("customer_id", "The customer key."))
	down := node("p", "stg", []string{"seed.p.raw"}, col("CUSTOMER_ID", ""))
	r := resolverFor(t, nil, project("p", up, down))

	got := columnDoc(t, r.Resolve(down, Existing{}), "CUSTOMER_ID")
	if got.Description != "The customer key." {
		t.Errorf("description = %q, want the upstream text despite the case difference", got.Description)
	}
	if got.Name != "CUSTOMER_ID" {
		t.Errorf("name = %q, want the warehouse spelling preserved", got.Name)
	}
}

func TestCaseSensitiveMatchingCanBeTurnedOn(t *testing.T) {
	up := seed("p", "raw", col("customer_id", "The customer key."))
	down := node("p", "stg", []string{"seed.p.raw"}, col("CUSTOMER_ID", ""))
	off := false
	r := resolverFor(t, func(c *config.Config) { c.Inheritance.CaseInsensitive = &off },
		project("p", up, down))

	got := columnDoc(t, r.Resolve(down, Existing{}), "CUSTOMER_ID")
	if got.SetDescription {
		t.Errorf("description = %q, want no match when matching case sensitively", got.Description)
	}
}

// --- progenitor ------------------------------------------------------------

func TestProgenitorIsRecordedAndCarriedForward(t *testing.T) {
	root := seed("p", "raw", col("id", "From the root."))
	// The middle model already carries a progenitor annotation from an earlier
	// run; the annotation must point past it to the true origin.
	mid := node("p", "mid", []string{"seed.p.raw"},
		col("id", "From the root.", withMeta(config.DefaultProgenitorKey, "seed.p.raw")))
	down := node("p", "stg", []string{"mid"}, col("id", ""))
	on := true
	r := resolverFor(t, func(c *config.Config) { c.Inheritance.Progenitor = &on },
		project("p", root, mid, down))

	got := columnDoc(t, r.Resolve(down, Existing{}), "id")
	if got.Progenitor != "seed.p.raw" {
		t.Errorf("progenitor = %q, want the original origin, not the intermediate", got.Progenitor)
	}
	if metaMap(got.Meta)[config.DefaultProgenitorKey] != "seed.p.raw" {
		t.Errorf("meta = %v, want the progenitor recorded", metaMap(got.Meta))
	}
}

// Provenance is on by default: an inherited description is the one line in a
// schema file nobody wrote, so it says where it came from.
func TestProgenitorIsRecordedByDefault(t *testing.T) {
	up := seed("p", "raw", col("id", "Upstream."))
	down := node("p", "stg", []string{"seed.p.raw"}, col("id", ""))
	r := resolverFor(t, nil, project("p", up, down))

	got := columnDoc(t, r.Resolve(down, Existing{}), "id")
	if metaMap(got.Meta)[config.DefaultProgenitorKey] != "seed.p.raw" {
		t.Errorf("meta = %v, want the progenitor recorded by default", metaMap(got.Meta))
	}
}

// A column that was documented locally has no progenitor to record, so it must
// not gain a meta key saying otherwise.
func TestProgenitorIsNotRecordedForALocalDescription(t *testing.T) {
	up := seed("p", "raw", col("id", "Upstream."))
	down := node("p", "stg", []string{"seed.p.raw"}, col("id", "Written here."))
	r := resolverFor(t, nil, project("p", up, down))

	got := columnDoc(t, r.Resolve(down, Existing{}), "id")
	if _, ok := metaMap(got.Meta)[config.DefaultProgenitorKey]; ok {
		t.Errorf("meta = %v, want no progenitor on a local description", metaMap(got.Meta))
	}
}

// Turning it off restores dbt-osmosis' default, which is what parity needs.
func TestProgenitorCanBeTurnedOff(t *testing.T) {
	up := seed("p", "raw", col("id", "Upstream."))
	down := node("p", "stg", []string{"seed.p.raw"}, col("id", ""))
	off := false
	r := resolverFor(t, func(c *config.Config) { c.Inheritance.Progenitor = &off },
		project("p", up, down))

	got := columnDoc(t, r.Resolve(down, Existing{}), "id")
	if _, ok := metaMap(got.Meta)[config.DefaultProgenitorKey]; ok {
		t.Errorf("meta = %v, want no progenitor key when off", metaMap(got.Meta))
	}
}

// --- column set management -------------------------------------------------

func TestStaleColumnsAreDroppedAndMissingOnesAdded(t *testing.T) {
	down := node("p", "stg", nil, col("kept", ""), col("added", ""))
	p := project("p", down)
	catalogFor(p, down, "kept", "added")
	r := resolverFor(t, nil, p)

	doc := r.Resolve(down, Existing{Columns: []ExistingColumn{
		{Name: "kept"}, {Name: "gone"},
	}})

	if !reflect.DeepEqual(doc.Drop, []string{"gone"}) {
		t.Errorf("drop = %v, want [gone]", doc.Drop)
	}
	got := columnDoc(t, doc, "added")
	if got.Existing {
		t.Error("the added column was reported as already present")
	}
}

// Without a catalog the manifest is not warehouse truth, so nothing is removed:
// deleting documentation on the strength of a guess is not acceptable.
func TestNothingIsDroppedWithoutWarehouseTruth(t *testing.T) {
	down := node("p", "stg", nil)
	r := resolverFor(t, nil, project("p", down))

	doc := r.Resolve(down, Existing{Columns: []ExistingColumn{{Name: "mystery", Description: "Kept."}}})
	if len(doc.Drop) != 0 {
		t.Errorf("drop = %v, want nothing dropped when the column set is unknown", doc.Drop)
	}
}

func TestRemoveStaleCanBeDisabled(t *testing.T) {
	down := node("p", "stg", nil, col("kept", ""))
	p := project("p", down)
	catalogFor(p, down, "kept")
	off := false
	r := resolverFor(t, func(c *config.Config) { c.Columns.RemoveStale = &off }, p)

	doc := r.Resolve(down, Existing{Columns: []ExistingColumn{{Name: "kept"}, {Name: "gone"}}})
	if len(doc.Drop) != 0 {
		t.Errorf("drop = %v, want nothing dropped", doc.Drop)
	}
}

func TestAddMissingCanBeDisabled(t *testing.T) {
	down := node("p", "stg", nil, col("kept", ""), col("added", ""))
	off := false
	r := resolverFor(t, func(c *config.Config) { c.Columns.AddMissing = &off }, project("p", down))

	doc := r.Resolve(down, Existing{Columns: []ExistingColumn{{Name: "kept"}}})
	for _, c := range doc.Columns {
		if c.Name == "added" {
			t.Error("added a column while add_missing was off")
		}
	}
}

func TestAlphabeticalColumnOrder(t *testing.T) {
	down := node("p", "stg", nil, col("zulu", ""), col("alpha", ""), col("mike", ""))
	order := config.OrderAlphabetical
	r := resolverFor(t, func(c *config.Config) { c.Columns.Order = &order }, project("p", down))

	doc := r.Resolve(down, Existing{})
	if want := []string{"alpha", "mike", "zulu"}; !reflect.DeepEqual(columnNames(doc), want) {
		t.Errorf("order = %v, want %v", columnNames(doc), want)
	}
}

// --- graph shape -----------------------------------------------------------

// A diamond reaches the shared root by two routes.
func TestDiamondVisitsTheSharedRootOnce(t *testing.T) {
	root := seed("p", "raw", col("id", "Root."))
	left := node("p", "left", []string{"seed.p.raw"}, col("id", ""))
	right := node("p", "right", []string{"seed.p.raw"}, col("id", ""))
	down := node("p", "stg", []string{"left", "right"}, col("id", ""))
	g := BuildGraph([]*dbt.Project{project("p", root, left, right, down)})

	gens := g.Generations(down.UniqueID)
	if len(gens) != 2 {
		t.Fatalf("got %d generations, want 2", len(gens))
	}
	if got := ids(gens[0]); !reflect.DeepEqual(got, []string{"model.p.left", "model.p.right"}) {
		t.Errorf("generation 1 = %v", got)
	}
	if got := ids(gens[1]); !reflect.DeepEqual(got, []string{"seed.p.raw"}) {
		t.Errorf("generation 2 = %v, want the root recorded once", got)
	}
}

func TestCycleDoesNotHang(t *testing.T) {
	a := node("p", "a", []string{"b"}, col("id", ""))
	b := node("p", "b", []string{"a"}, col("id", "From b."))
	g := BuildGraph([]*dbt.Project{project("p", a, b)})

	gens := g.Generations(a.UniqueID)
	if len(gens) != 1 || len(gens[0]) != 1 || gens[0][0].UniqueID != "model.p.b" {
		t.Fatalf("generations = %v, want a single generation holding b", gens)
	}
}

func TestNonDocumentableDependenciesAreIgnored(t *testing.T) {
	up := seed("p", "raw", col("id", "Upstream."))
	down := node("p", "stg", []string{"seed.p.raw", "test.p.not_null_stg_id.abc123"}, col("id", ""))
	g := BuildGraph([]*dbt.Project{project("p", up, down)})

	if got := g.Parents(down.UniqueID); !reflect.DeepEqual(got, []string{"seed.p.raw"}) {
		t.Errorf("parents = %v, want the test dependency dropped", got)
	}
}

// --- cross-project (the dbt-loom half) -------------------------------------

// The producer's manifest is loaded alongside the consumer's, so a ref that dbt
// recorded against another project resolves and its documentation flows down.
func TestCrossProjectInheritance(t *testing.T) {
	upstream := node("platform", "dim_customers", nil, col("customer_id", "The customer key."))
	upstream.Access = "public"
	producer := project("platform", upstream)

	// The consumer's manifest records the ref under its own package name, which
	// is exactly the case the unique_id lookup cannot resolve on its own.
	downstream := node("analytics", "report", []string{"model.analytics.dim_customers"}, col("customer_id", ""))
	consumer := project("analytics", downstream)

	r := resolverFor(t, nil, producer, consumer)
	got := columnDoc(t, r.Resolve(downstream, Existing{}), "customer_id")

	if got.Description != "The customer key." {
		t.Fatalf("description = %q, want the producer project's text", got.Description)
	}
	if got.Progenitor != "model.platform.dim_customers" {
		t.Errorf("progenitor = %q, want the producer's node", got.Progenitor)
	}
}

// dbt only lets another project reference a public model, and neither should
// dbt-ditto: a private model must not leak its documentation across the seam.
func TestCrossProjectRefusesPrivateModels(t *testing.T) {
	upstream := node("platform", "dim_customers", nil, col("customer_id", "The customer key."))
	upstream.Access = "protected"
	producer := project("platform", upstream)

	downstream := node("analytics", "report", []string{"model.analytics.dim_customers"}, col("customer_id", ""))
	consumer := project("analytics", downstream)

	r := resolverFor(t, nil, producer, consumer)
	if got := columnDoc(t, r.Resolve(downstream, Existing{}), "customer_id"); got.SetDescription {
		t.Errorf("description = %q, want nothing inherited from a non-public model", got.Description)
	}
}

// A same-named model in each project must not be mistaken for the other: a
// local dependency always resolves locally.
func TestLocalNamesDoNotResolveAcrossProjects(t *testing.T) {
	foreign := node("platform", "stg_orders", nil, col("id", "The producer's text."))
	foreign.Access = "public"
	producer := project("platform", foreign)

	local := node("analytics", "stg_orders", nil, col("id", "The consumer's own text."))
	down := node("analytics", "report", []string{"stg_orders"}, col("id", ""))
	consumer := project("analytics", local, down)

	r := resolverFor(t, nil, producer, consumer)
	got := columnDoc(t, r.Resolve(down, Existing{}), "id")
	if got.Description != "The consumer's own text." {
		t.Errorf("description = %q, want the consumer's own upstream", got.Description)
	}
}

func ids(nodes []*dbt.Node) []string {
	out := make([]string, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, n.UniqueID)
	}
	return out
}
