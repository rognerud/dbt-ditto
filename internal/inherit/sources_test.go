package inherit

import (
	"testing"

	"github.com/rognerud/dbt-ditto/internal/config"
	"github.com/rognerud/dbt-ditto/internal/dbt"
)

// source builds a source node, the one kind of node that can never inherit
// anything the ordinary way: it is a root of the DAG.
func source(pkg, sourceName, table string, cols ...*dbt.Column) *dbt.Node {
	n := &dbt.Node{
		UniqueID:     "source." + pkg + "." + sourceName + "." + table,
		Name:         table,
		ResourceType: "source",
		PackageName:  pkg,
		SourceName:   sourceName,
		Identifier:   table,
		Columns:      map[string]*dbt.Column{},
	}
	for _, c := range cols {
		n.Columns[c.Name] = c
	}
	return n
}

// attach puts extra nodes into a project that already exists.
func attach(p *dbt.Project, nodes ...*dbt.Node) {
	for _, n := range nodes {
		n.Project = p
		n.Manifest = p.Manifest
		if n.IsSource() {
			p.Manifest.Sources[n.UniqueID] = n
		} else {
			p.Manifest.Nodes[n.UniqueID] = n
		}
	}
}

// catalogWithComments gives a node a catalog entry whose columns carry
// warehouse comments, as Snowflake or BigQuery would report them.
func catalogWithComments(p *dbt.Project, n *dbt.Node, comments map[string]string, order ...string) {
	if p.Catalog == nil {
		p.Catalog = &dbt.Catalog{
			Nodes:   map[string]*dbt.CatalogNode{},
			Sources: map[string]*dbt.CatalogNode{},
		}
	}
	entry := &dbt.CatalogNode{Columns: map[string]dbt.CatalogColumn{}}
	for i, name := range order {
		entry.Columns[name] = dbt.CatalogColumn{
			Name: name, Index: i, Type: "VARCHAR", Comment: comments[name],
		}
	}
	if n.IsSource() {
		p.Catalog.Sources[n.UniqueID] = entry
	} else {
		p.Catalog.Nodes[n.UniqueID] = entry
	}
}

func backfillOn(c *config.Config) {
	on := true
	c.Inheritance.Backfill.Enabled = &on
}

// --- warehouse comments ----------------------------------------------------

// A source has nothing above it, so the warehouse's own comment is often the
// only documentation that exists.
func TestWarehouseCommentDocumentsANewColumn(t *testing.T) {
	src := source("p", "billing", "raw_invoices")
	p := project("p")
	attach(p, src)
	catalogWithComments(p, src,
		map[string]string{"invoice_id": "Surrogate key for an invoice."},
		"invoice_id", "order_id")

	r := resolverFor(t, nil, p)
	doc := r.Resolve(src, Existing{})

	if got := columnDoc(t, doc, "invoice_id"); got.Description != "Surrogate key for an invoice." {
		t.Errorf("description = %q, want the warehouse comment", got.Description)
	}
	if got := columnDoc(t, doc, "order_id"); got.SetDescription {
		t.Errorf("description = %q, want none: the warehouse has no comment for it", got.Description)
	}
}

// By default a column already written about is left alone, which is what
// dbt-osmosis does: it only reads the comment for a column it is adding.
func TestWarehouseCommentDoesNotFillAnExistingColumnByDefault(t *testing.T) {
	src := source("p", "billing", "raw_invoices", col("invoice_id", ""))
	p := project("p")
	attach(p, src)
	catalogWithComments(p, src,
		map[string]string{"invoice_id": "Surrogate key for an invoice."}, "invoice_id")

	r := resolverFor(t, nil, p)
	if got := columnDoc(t, r.Resolve(src, Existing{
		Columns: []ExistingColumn{{Name: "invoice_id"}},
	}), "invoice_id"); got.SetDescription {
		t.Errorf("description = %q, want the existing entry left alone", got.Description)
	}
}

// `comments: always` is for a project that documents its sources in the
// warehouse and wants that to reach the YAML even for columns already listed.
func TestWarehouseCommentFillsExistingColumnWhenAsked(t *testing.T) {
	src := source("p", "billing", "raw_invoices", col("invoice_id", ""))
	p := project("p")
	attach(p, src)
	catalogWithComments(p, src,
		map[string]string{"invoice_id": "Surrogate key for an invoice."}, "invoice_id")

	always := config.WarehouseCommentsAlways
	r := resolverFor(t, func(c *config.Config) { c.Columns.Comments = &always }, p)
	got := columnDoc(t, r.Resolve(src, Existing{
		Columns: []ExistingColumn{{Name: "invoice_id"}},
	}), "invoice_id")
	if got.Description != "Surrogate key for an invoice." {
		t.Errorf("description = %q, want the warehouse comment", got.Description)
	}
}

// A description written by hand always beats the warehouse comment.
func TestHandWrittenDescriptionBeatsTheWarehouseComment(t *testing.T) {
	src := source("p", "billing", "raw_invoices", col("invoice_id", "What we actually mean."))
	p := project("p")
	attach(p, src)
	catalogWithComments(p, src,
		map[string]string{"invoice_id": "Whatever the DBA typed."}, "invoice_id")

	always := config.WarehouseCommentsAlways
	r := resolverFor(t, func(c *config.Config) { c.Columns.Comments = &always }, p)
	if got := columnDoc(t, r.Resolve(src, Existing{
		Columns: []ExistingColumn{{Name: "invoice_id", Description: "What we actually mean."}},
	}), "invoice_id"); got.SetDescription {
		t.Errorf("description = %q, want the hand-written text kept", got.Description)
	}
}

func TestWarehouseCommentsCanBeTurnedOff(t *testing.T) {
	src := source("p", "billing", "raw_invoices")
	p := project("p")
	attach(p, src)
	catalogWithComments(p, src, map[string]string{"invoice_id": "From the warehouse."}, "invoice_id")

	never := config.WarehouseCommentsNever
	r := resolverFor(t, func(c *config.Config) { c.Columns.Comments = &never }, p)
	if got := columnDoc(t, r.Resolve(src, Existing{}), "invoice_id"); got.SetDescription {
		t.Errorf("description = %q, want none", got.Description)
	}
}

// --- backfill from downstream ----------------------------------------------

// The documentation for a raw source usually exists exactly one step
// downstream, in the staging model that reads it.
func TestSourceIsBackfilledFromItsStagingModel(t *testing.T) {
	src := source("p", "billing", "raw_invoices")
	stg := node("p", "stg_invoices", []string{"source.p.billing.raw_invoices"},
		col("invoice_id", "Surrogate key for an invoice."))
	p := project("p")
	attach(p, src, stg)
	catalogWithComments(p, src, nil, "invoice_id")

	r := resolverFor(t, backfillOn, p)
	got := columnDoc(t, r.Resolve(src, Existing{}), "invoice_id")
	if got.Description != "Surrogate key for an invoice." {
		t.Fatalf("description = %q, want it carried up from the staging model", got.Description)
	}
	if got.Progenitor != "model.p.stg_invoices" {
		t.Errorf("progenitor = %q, want the model it came from", got.Progenitor)
	}
}

// A staging model often selects a column straight through without documenting
// it. The search has to keep going rather than stop at the first descendant.
func TestBackfillLooksPastAnUndocumentedDescendant(t *testing.T) {
	src := source("p", "billing", "raw_invoices")
	stg := node("p", "stg_invoices", []string{"source.p.billing.raw_invoices"},
		col("invoice_id", ""))
	mart := node("p", "dim_invoices", []string{"stg_invoices"},
		col("invoice_id", "Surrogate key for an invoice."))
	p := project("p")
	attach(p, src, stg, mart)
	catalogWithComments(p, src, nil, "invoice_id")

	r := resolverFor(t, backfillOn, p)
	got := columnDoc(t, r.Resolve(src, Existing{}), "invoice_id")
	if got.Description != "Surrogate key for an invoice." {
		t.Fatalf("description = %q, want the description from two steps down", got.Description)
	}
}

// Backfill only ever fills a blank. It must not overwrite anything.
func TestBackfillNeverOverwrites(t *testing.T) {
	src := source("p", "billing", "raw_invoices", col("invoice_id", "The source's own words."))
	stg := node("p", "stg_invoices", []string{"source.p.billing.raw_invoices"},
		col("invoice_id", "The model's words."))
	p := project("p")
	attach(p, src, stg)
	catalogWithComments(p, src, nil, "invoice_id")

	r := resolverFor(t, backfillOn, p)
	if got := columnDoc(t, r.Resolve(src, Existing{
		Columns: []ExistingColumn{{Name: "invoice_id", Description: "The source's own words."}},
	}), "invoice_id"); got.SetDescription {
		t.Errorf("description = %q, want the source's own text kept", got.Description)
	}
}

// A warehouse comment is the source's own documentation, so it wins over
// anything found downstream.
func TestWarehouseCommentBeatsBackfill(t *testing.T) {
	src := source("p", "billing", "raw_invoices")
	stg := node("p", "stg_invoices", []string{"source.p.billing.raw_invoices"},
		col("invoice_id", "The model's words."))
	p := project("p")
	attach(p, src, stg)
	catalogWithComments(p, src, map[string]string{"invoice_id": "The warehouse's words."}, "invoice_id")

	r := resolverFor(t, backfillOn, p)
	got := columnDoc(t, r.Resolve(src, Existing{}), "invoice_id")
	if got.Description != "The warehouse's words." {
		t.Errorf("description = %q, want the source's own comment to win", got.Description)
	}
}

// Backfill is limited to sources by default: a mart's descriptions should not
// start flowing backwards into the models that feed it.
func TestBackfillIsLimitedToSourcesByDefault(t *testing.T) {
	stg := node("p", "stg_invoices", nil, col("invoice_id", ""))
	mart := node("p", "dim_invoices", []string{"stg_invoices"},
		col("invoice_id", "The mart's words."))
	p := project("p", stg, mart)
	catalogWithComments(p, stg, nil, "invoice_id")

	r := resolverFor(t, backfillOn, p)
	if got := columnDoc(t, r.Resolve(stg, Existing{}), "invoice_id"); got.SetDescription {
		t.Errorf("description = %q, want models left out of backfill by default", got.Description)
	}

	off := false
	r = resolverFor(t, func(c *config.Config) {
		backfillOn(c)
		c.Inheritance.Backfill.SourcesOnly = &off
	}, p)
	if got := columnDoc(t, r.Resolve(stg, Existing{}), "invoice_id"); got.Description != "The mart's words." {
		t.Errorf("description = %q, want backfill to reach models once asked", got.Description)
	}
}

func TestBackfillIsOffByDefault(t *testing.T) {
	src := source("p", "billing", "raw_invoices")
	stg := node("p", "stg_invoices", []string{"source.p.billing.raw_invoices"},
		col("invoice_id", "The model's words."))
	p := project("p")
	attach(p, src, stg)
	catalogWithComments(p, src, nil, "invoice_id")

	r := resolverFor(t, nil, p)
	if got := columnDoc(t, r.Resolve(src, Existing{}), "invoice_id"); got.SetDescription {
		t.Errorf("description = %q, want none while backfill is off", got.Description)
	}
}

// Descendants mirrors Generations: nearest first, sorted, each node once.
func TestDescendantsAreGroupedByDistance(t *testing.T) {
	src := source("p", "billing", "raw_invoices")
	stg := node("p", "stg_invoices", []string{"source.p.billing.raw_invoices"})
	other := node("p", "stg_other", []string{"source.p.billing.raw_invoices"})
	mart := node("p", "dim_invoices", []string{"stg_invoices", "stg_other"})
	p := project("p")
	attach(p, src, stg, other, mart)

	g := BuildGraph([]*dbt.Project{p})
	gens := g.Descendants(src.UniqueID)
	if len(gens) != 2 {
		t.Fatalf("got %d generations, want 2: %v", len(gens), gens)
	}
	if got := ids(gens[0]); len(got) != 2 || got[0] != "model.p.stg_invoices" {
		t.Errorf("generation 1 = %v", got)
	}
	if got := ids(gens[1]); len(got) != 1 || got[0] != "model.p.dim_invoices" {
		t.Errorf("generation 2 = %v, want the mart recorded once", got)
	}
}

// Backfill and derived matching compose: a source column that only exists
// downstream under an aggregated name is still found.
func TestBackfillUsesDerivedMatchingWhenEnabled(t *testing.T) {
	src := source("p", "billing", "raw_invoices")
	stg := node("p", "stg_invoices", []string{"source.p.billing.raw_invoices"},
		col("total_invoice_total_cents", "Invoice total in minor units."))
	p := project("p")
	attach(p, src, stg)
	catalogWithComments(p, src, nil, "invoice_total_cents")

	r := resolverFor(t, func(c *config.Config) {
		backfillOn(c)
		derivedOn(c)
	}, p)
	got := columnDoc(t, r.Resolve(src, Existing{}), "invoice_total_cents")
	if got.Description != "Invoice total in minor units." {
		t.Errorf("description = %q, want the aggregated descendant matched", got.Description)
	}
}
