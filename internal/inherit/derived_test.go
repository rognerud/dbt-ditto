package inherit

import (
	"reflect"
	"testing"

	"github.com/rognerud/dbt-ditto/internal/config"
	"github.com/rognerud/dbt-ditto/internal/dbt"
)

// derivedOn enables cross-name matching, off by default as beyond dbt-osmosis.
func derivedOn(c *config.Config) {
	on := true
	c.Inheritance.Derived.Enabled = &on
}

func TestDerivedIsOffByDefault(t *testing.T) {
	r := (&config.Config{}).Resolve()
	if r.DerivedStructs || r.DerivedAggregates {
		t.Error("derived matching must be off unless asked for, or dbt-osmosis parity breaks")
	}
	if !r.ExpandStructs {
		t.Error("struct expansion is what dbt-osmosis does and must be on by default")
	}
}

func TestDerivedEnabledTurnsOnBothStrategies(t *testing.T) {
	c := &config.Config{}
	derivedOn(c)
	r := c.Resolve()
	if !r.DerivedStructs || !r.DerivedAggregates {
		t.Fatalf("enabled: true should turn both on, got structs=%v aggregates=%v",
			r.DerivedStructs, r.DerivedAggregates)
	}
	if len(r.DerivedPrefixes) == 0 || len(r.DerivedSuffixes) == 0 {
		t.Error("the default aggregate word lists were not applied")
	}
}

// A summed column still means what the column it was summed from means.
func TestAggregatedColumnInheritsFromItsSource(t *testing.T) {
	up := seed("p", "raw", col("amount_cents", "Order gross value in minor units."))
	down := node("p", "agg", []string{"seed.p.raw"}, col("total_amount_cents", ""))
	r := resolverFor(t, derivedOn, project("p", up, down))

	got := columnDoc(t, r.Resolve(down, Existing{}), "total_amount_cents")
	if got.Description != "Order gross value in minor units." {
		t.Fatalf("description = %q, want it carried across the aggregation", got.Description)
	}
}

func TestAggregateSuffixesAndBothEnds(t *testing.T) {
	up := seed("p", "raw", col("amount_cents", "The amount."))
	cases := map[string]string{
		"amount_cents_sum":       "suffix",
		"total_amount_cents":     "prefix",
		"total_amount_cents_sum": "both ends",
		"max_amount_cents":       "prefix",
		"amount_cents_avg":       "suffix",
	}
	for name, why := range cases {
		down := node("p", "agg_"+name, []string{"seed.p.raw"}, col(name, ""))
		r := resolverFor(t, derivedOn, project("p", up, down))
		got := columnDoc(t, r.Resolve(down, Existing{}), name)
		if got.Description != "The amount." {
			t.Errorf("%s (%s): description = %q, want it inherited", name, why, got.Description)
		}
	}
}

// Only whole words are stripped: `counterparty_id` is not `erparty_id` summed.
func TestAggregateStrippingOnlyTakesWholeWords(t *testing.T) {
	up := seed("p", "raw", col("erparty_id", "Nonsense that must not be inherited."))
	down := node("p", "stg", []string{"seed.p.raw"}, col("counterparty_id", ""))
	r := resolverFor(t, derivedOn, project("p", up, down))

	if got := columnDoc(t, r.Resolve(down, Existing{}), "counterparty_id"); got.SetDescription {
		t.Errorf("description = %q, want none: `count` is not a word here", got.Description)
	}
}

// Packing into a struct: the field keeps the meaning of the flat column.
func TestStructFieldInheritsFromTheFlatColumn(t *testing.T) {
	up := seed("p", "raw", col("first_name", "Customer given name."))
	down := node("p", "packed", []string{"seed.p.raw"}, col("profile.first_name", ""))
	r := resolverFor(t, derivedOn, project("p", up, down))

	got := columnDoc(t, r.Resolve(down, Existing{}), "profile.first_name")
	if got.Description != "Customer given name." {
		t.Fatalf("description = %q, want it carried into the struct", got.Description)
	}
}

// And the reverse: unpacking a struct back into flat columns.
func TestFlatColumnInheritsFromTheStructField(t *testing.T) {
	up := seed("p", "raw", col("profile.first_name", "Customer given name."))
	down := node("p", "flat", []string{"seed.p.raw"}, col("first_name", ""))
	r := resolverFor(t, derivedOn, project("p", up, down))

	got := columnDoc(t, r.Resolve(down, Existing{}), "first_name")
	if got.Description != "Customer given name." {
		t.Fatalf("description = %q, want it carried out of the struct", got.Description)
	}
}

// Struct nesting and aggregation at once.
func TestStructFieldAggregateInheritsFromTheFlatColumn(t *testing.T) {
	up := seed("p", "raw", col("amount_cents", "The amount."))
	down := node("p", "packed", []string{"seed.p.raw"}, col("totals.total_amount_cents", ""))
	r := resolverFor(t, derivedOn, project("p", up, down))

	got := columnDoc(t, r.Resolve(down, Existing{}), "totals.total_amount_cents")
	if got.Description != "The amount." {
		t.Fatalf("description = %q, want both strategies to combine", got.Description)
	}
}

// An exact match must always beat a derived one, however tempting the guess.
func TestExactMatchBeatsDerivedMatch(t *testing.T) {
	up := seed("p", "raw",
		col("amount_cents", "The per-order amount."),
		col("total_amount_cents", "The pre-computed total."),
	)
	down := node("p", "agg", []string{"seed.p.raw"}, col("total_amount_cents", ""))
	r := resolverFor(t, derivedOn, project("p", up, down))

	got := columnDoc(t, r.Resolve(down, Existing{}), "total_amount_cents")
	if got.Description != "The pre-computed total." {
		t.Fatalf("description = %q, want the exactly-named column to win", got.Description)
	}
}

// Sources are inheritance roots too, so derived matching has to reach them.
func TestDerivedMatchingWorksFromSources(t *testing.T) {
	src := &dbt.Node{
		UniqueID: "source.p.crm.raw_orders", Name: "raw_orders", ResourceType: "source",
		PackageName: "p", SourceName: "crm", Identifier: "raw_orders",
		Columns: map[string]*dbt.Column{
			"amount_cents": col("amount_cents", "Order gross value, from the CRM."),
		},
	}
	down := node("p", "agg", []string{"source.p.crm.raw_orders"}, col("total_amount_cents", ""))

	p := project("p", down)
	src.Project = p
	src.Manifest = p.Manifest
	p.Manifest.Sources[src.UniqueID] = src

	r := resolverFor(t, derivedOn, p)
	got := columnDoc(t, r.Resolve(down, Existing{}), "total_amount_cents")
	if got.Description != "Order gross value, from the CRM." {
		t.Fatalf("description = %q, want it inherited from the source", got.Description)
	}
}

// With derived matching off none of this happens, which is the parity claim.
func TestDerivedMatchingDoesNothingWhenOff(t *testing.T) {
	up := seed("p", "raw", col("amount_cents", "The amount."))
	down := node("p", "agg", []string{"seed.p.raw"}, col("total_amount_cents", ""))
	r := resolverFor(t, nil, project("p", up, down))

	if got := columnDoc(t, r.Resolve(down, Existing{}), "total_amount_cents"); got.SetDescription {
		t.Errorf("description = %q, want none while derived matching is off", got.Description)
	}
}

// An ancestor whose columns exist only in the catalog does not block a derived
// match from reaching further up.
//
// The matcher indexes n.Columns, which is the manifest; a catalog-only column is
// reached instead by the EffectiveColumn fallback in matcher.find, and only under
// its exact name. That asymmetry is deliberate. A catalog-backed column carries no
// documentation at all — EffectiveColumn returns the shared empty sentinel, since
// a catalog records a column's existence, type and ordinal, not anyone's prose.
// What that sentinel buys is the claim in inheritInto: the first ancestor in a
// generation that has the column takes it and the rest of the generation is
// skipped, as dbt-osmosis does. So it shadows siblings, not ancestors. Under an
// exact name that claim is right. Under a derived name the column was renamed on
// the way down, which is the case this feature exists to follow, so a relation
// that merely happens to hold a similarly named column must not stop it.
func TestACatalogOnlyAncestorDoesNotShadowADerivedMatch(t *testing.T) {
	documented := seed("p", "raw", col("amount_cents", "Order gross value."))
	// No manifest columns: `dbt docs generate` ran, nobody wrote YAML.
	passthrough := node("p", "stg", []string{"seed.p.raw"})
	down := node("p", "agg", []string{"model.p.stg"}, col("total_amount_cents", ""))

	p := project("p", documented, passthrough, down)
	catalogFor(p, passthrough, "amount_cents")

	r := resolverFor(t, derivedOn, p)
	got := columnDoc(t, r.Resolve(down, Existing{}), "total_amount_cents")
	if got.Description != "Order gross value." {
		t.Fatalf("description = %q, want the rename followed past the undocumented "+
			"catalog-only model to the seed that documents it", got.Description)
	}
}

// The other half of the same rule, and what the EffectiveColumn fallback is
// actually for. Within one generation the first ancestor that has the column
// claims it and the rest of the generation is skipped, so a catalog-only parent
// shadows its *siblings* — it does not shadow its own ancestors, because
// generations are folded furthest-first and an empty description never overwrites
// one. Under an exact name that claim stands.
func TestACatalogOnlyParentShadowsItsSiblingsUnderAnExactName(t *testing.T) {
	// Both parents are the same generation; unique_id orders stg_a first.
	bare := node("p", "stg_a", nil)
	documented := node("p", "stg_b", nil, col("amount_cents", "Order gross value."))
	down := node("p", "agg", []string{"model.p.stg_a", "model.p.stg_b"},
		col("amount_cents", ""))

	p := project("p", bare, documented, down)
	catalogFor(p, bare, "amount_cents")

	r := resolverFor(t, derivedOn, p)
	if got := columnDoc(t, r.Resolve(down, Existing{}), "amount_cents"); got.SetDescription {
		t.Fatalf("description = %q, want none: the first parent of the generation has "+
			"the column and does not document it", got.Description)
	}
}

// And under a derived name the same catalog-only parent does not claim it, so the
// documented sibling is reached. This is the asymmetry stated above, in the one
// place where it changes an answer.
func TestACatalogOnlyParentDoesNotShadowSiblingsUnderADerivedName(t *testing.T) {
	bare := node("p", "stg_a", nil)
	documented := node("p", "stg_b", nil, col("amount_cents", "Order gross value."))
	down := node("p", "agg", []string{"model.p.stg_a", "model.p.stg_b"},
		col("total_amount_cents", ""))

	p := project("p", bare, documented, down)
	catalogFor(p, bare, "amount_cents")

	r := resolverFor(t, derivedOn, p)
	got := columnDoc(t, r.Resolve(down, Existing{}), "total_amount_cents")
	if got.Description != "Order gross value." {
		t.Fatalf("description = %q, want the documented sibling reached: the renamed "+
			"column is not the catalog-only parent's column", got.Description)
	}
}

// --- struct type parsing ---------------------------------------------------

func TestStructFieldExpansion(t *testing.T) {
	cases := []struct {
		name     string
		column   string
		dataType string
		want     []string
	}{
		{
			name:     "duckdb",
			column:   "profile",
			dataType: "STRUCT(first_name VARCHAR, last_name VARCHAR)",
			want:     []string{"profile.first_name", "profile.last_name"},
		},
		{
			name:     "bigquery angle brackets",
			column:   "profile",
			dataType: "STRUCT<first_name STRING, last_name STRING>",
			want:     []string{"profile.first_name", "profile.last_name"},
		},
		{
			name:     "nested",
			column:   "c",
			dataType: "STRUCT(name STRUCT(first VARCHAR, last VARCHAR), age INTEGER)",
			want:     []string{"c.name", "c.name.first", "c.name.last", "c.age"},
		},
		{
			name:     "repeated record",
			column:   "items",
			dataType: "STRUCT(sku VARCHAR)[]",
			want:     []string{"items.sku"},
		},
		{
			name:     "array of struct",
			column:   "items",
			dataType: "ARRAY<STRUCT<sku STRING>>",
			want:     []string{"items.sku"},
		},
		{
			name:     "quoted field name",
			column:   "p",
			dataType: `STRUCT("order" VARCHAR, n INTEGER)`,
			want:     []string{"p.order", "p.n"},
		},
		{
			name:     "commas inside a field type",
			column:   "p",
			dataType: "STRUCT(amount DECIMAL(38, 9), name VARCHAR)",
			want:     []string{"p.amount", "p.name"},
		},
		{"not a struct", "n", "INTEGER", nil},
		{"empty type", "n", "", nil},
		{"malformed", "n", "STRUCT(", nil},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var got []string
			for _, f := range dbt.StructFields(c.column, c.dataType) {
				got = append(got, f.Path)
			}
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("fields = %v, want %v", got, c.want)
			}
		})
	}
}

func TestStructFieldTypesAreKept(t *testing.T) {
	fields := dbt.StructFields("p", "STRUCT(amount DECIMAL(38, 9), name VARCHAR)")
	if len(fields) != 2 {
		t.Fatalf("got %d fields, want 2", len(fields))
	}
	if fields[0].Type != "DECIMAL(38, 9)" {
		t.Errorf("type = %q, want the full type including its own commas", fields[0].Type)
	}
}
