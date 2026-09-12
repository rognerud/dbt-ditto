package inherit

import (
	"strings"
	"testing"

	"github.com/rognerud/dbt-ditto/internal/config"
)

// --- directives ------------------------------------------------------------

// The case name matching cannot solve: the column was renamed on the way down,
// so nothing links `cust_id` to `customer_id` except the analyst saying so.
func TestDirectivePointsAtARenamedColumn(t *testing.T) {
	up := seed("p", "raw", col("customer_id", "Surrogate key of the customer."))
	down := node("p", "stg", []string{"seed.p.raw"},
		col("cust_id", "Inherited: raw.customer_id"))
	r := resolverFor(t, nil, project("p", up, down))

	doc := r.Resolve(down, Existing{})
	got := columnDoc(t, doc, "cust_id")

	if got.Description != "Surrogate key of the customer." {
		t.Errorf("description = %q, want the directive followed", got.Description)
	}
	if got.Progenitor != "seed.p.raw" {
		t.Errorf("progenitor = %q, want the node the directive named", got.Progenitor)
	}
	if len(doc.Warnings) != 0 {
		t.Errorf("warnings = %v, want none", doc.Warnings)
	}
}

// A unique_id is accepted too, which is the only way to name a node whose
// resource name is not unique across projects.
func TestDirectiveAcceptsAUniqueID(t *testing.T) {
	up := seed("p", "raw", col("customer_id", "Surrogate key of the customer."))
	down := node("p", "stg", []string{"seed.p.raw"},
		col("cust_id", "Inherited: seed.p.raw.customer_id"))
	r := resolverFor(t, nil, project("p", up, down))

	if got := columnDoc(t, r.Resolve(down, Existing{}), "cust_id"); got.Description != "Surrogate key of the customer." {
		t.Errorf("description = %q, want the directive followed", got.Description)
	}
}

// The directive wins over ordinary inheritance: a column that also matches an
// upstream name by accident still takes what it was pointed at.
func TestDirectiveBeatsNameMatching(t *testing.T) {
	up := seed("p", "raw", col("id", "The wrong one."), col("customer_id", "The right one."))
	down := node("p", "stg", []string{"seed.p.raw"}, col("id", "Inherited: raw.customer_id"))
	r := resolverFor(t, nil, project("p", up, down))

	if got := columnDoc(t, r.Resolve(down, Existing{}), "id"); got.Description != "The right one." {
		t.Errorf("description = %q, want the directive to outrank the name match", got.Description)
	}
}

// An unresolvable directive is left in the file and reported. Dropping it would
// lose the instruction; writing it back without a word would look like prose.
func TestUnresolvableDirectiveWarnsAndIsKept(t *testing.T) {
	up := seed("p", "raw", col("customer_id", "Surrogate key of the customer."))
	down := node("p", "stg", []string{"seed.p.raw"},
		col("cust_id", "Inherited: raw.no_such_column"))
	r := resolverFor(t, nil, project("p", up, down))

	doc := r.Resolve(down, Existing{})
	if got := columnDoc(t, doc, "cust_id"); got.Description != "Inherited: raw.no_such_column" {
		t.Errorf("description = %q, want the directive left alone", got.Description)
	}
	if len(doc.Warnings) != 1 || doc.Warnings[0].Kind != WarnDirective {
		t.Fatalf("warnings = %v, want one directive warning", doc.Warnings)
	}
	if !strings.Contains(doc.Warnings[0].Detail, "no_such_column") {
		t.Errorf("detail = %q, want the column named", doc.Warnings[0].Detail)
	}
}

func TestDirectiveNamingAnUnknownNodeWarns(t *testing.T) {
	up := seed("p", "raw", col("customer_id", "Surrogate key of the customer."))
	down := node("p", "stg", []string{"seed.p.raw"}, col("cust_id", "Inherited: nowhere.customer_id"))
	r := resolverFor(t, nil, project("p", up, down))

	doc := r.Resolve(down, Existing{})
	if len(doc.Warnings) != 1 || doc.Warnings[0].Kind != WarnDirective {
		t.Fatalf("warnings = %v, want one directive warning", doc.Warnings)
	}
}

// Pointing at a column that exists but says nothing is a mistake worth hearing
// about: the analyst thinks that column is documented, and it is not.
func TestDirectiveToAnUndocumentedColumnWarns(t *testing.T) {
	up := seed("p", "raw", col("customer_id", ""))
	down := node("p", "stg", []string{"seed.p.raw"}, col("cust_id", "Inherited: raw.customer_id"))
	r := resolverFor(t, nil, project("p", up, down))

	doc := r.Resolve(down, Existing{})
	if len(doc.Warnings) != 1 || doc.Warnings[0].Kind != WarnDirective {
		t.Fatalf("warnings = %v, want one directive warning", doc.Warnings)
	}
}

// Prose that merely begins with the word is not a directive: there is no dot,
// so it cannot name a column.
func TestOrdinaryProseIsNotADirective(t *testing.T) {
	up := seed("p", "raw", col("id", "Upstream."))
	down := node("p", "stg", []string{"seed.p.raw"},
		col("id", "Inherited from the legacy system"))
	r := resolverFor(t, nil, project("p", up, down))

	doc := r.Resolve(down, Existing{})
	if got := columnDoc(t, doc, "id"); got.Description != "Inherited from the legacy system" {
		t.Errorf("description = %q, want the local prose kept", got.Description)
	}
	if len(doc.Warnings) != 0 {
		t.Errorf("warnings = %v, want none", doc.Warnings)
	}
}

func TestDirectivesCanBeTurnedOff(t *testing.T) {
	up := seed("p", "raw", col("customer_id", "Surrogate key of the customer."))
	down := node("p", "stg", []string{"seed.p.raw"}, col("cust_id", "Inherited: raw.customer_id"))
	off := false
	r := resolverFor(t, func(c *config.Config) { c.Inheritance.Directives = &off },
		project("p", up, down))

	doc := r.Resolve(down, Existing{})
	if got := columnDoc(t, doc, "cust_id"); got.Description != "Inherited: raw.customer_id" {
		t.Errorf("description = %q, want the directive treated as prose", got.Description)
	}
	if len(doc.Warnings) != 0 {
		t.Errorf("warnings = %v, want none when directives are off", doc.Warnings)
	}
}

func TestDirectivePrefixIsConfigurable(t *testing.T) {
	up := seed("p", "raw", col("customer_id", "Surrogate key of the customer."))
	down := node("p", "stg", []string{"seed.p.raw"}, col("cust_id", "see: raw.customer_id"))
	prefix := "see:"
	r := resolverFor(t, func(c *config.Config) { c.Inheritance.DirectivePrefix = &prefix },
		project("p", up, down))

	if got := columnDoc(t, r.Resolve(down, Existing{}), "cust_id"); got.Description != "Surrogate key of the customer." {
		t.Errorf("description = %q, want the custom prefix honoured", got.Description)
	}
}

// --- ambiguity -------------------------------------------------------------

// Two parents, same column, different descriptions: the winner is whichever
// unique_id sorts first, which is arbitrary, so say so.
func TestAmbiguousColumnIsReported(t *testing.T) {
	a := seed("p", "customers", col("id", "The customer's identifier."))
	b := seed("p", "orders", col("id", "The order's identifier."))
	down := node("p", "joined", []string{"seed.p.customers", "seed.p.orders"}, col("id", ""))
	r := resolverFor(t, nil, project("p", a, b, down))

	doc := r.Resolve(down, Existing{})
	got := columnDoc(t, doc, "id")

	if got.Description != "The customer's identifier." {
		t.Errorf("description = %q, want the first parent by unique_id", got.Description)
	}
	if len(doc.Warnings) != 1 || doc.Warnings[0].Kind != WarnAmbiguous {
		t.Fatalf("warnings = %v, want one ambiguity warning", doc.Warnings)
	}
	if w := doc.Warnings[0]; w.Column != "id" || !strings.Contains(w.Detail, "seed.p.orders") {
		t.Errorf("warning = %+v, want the losing parent named", w)
	}
}

// Parents that agree are not ambiguous, however many of them there are.
func TestParentsThatAgreeAreNotAmbiguous(t *testing.T) {
	a := seed("p", "customers", col("id", "The identifier."))
	b := seed("p", "orders", col("id", "The identifier."))
	down := node("p", "joined", []string{"seed.p.customers", "seed.p.orders"}, col("id", ""))
	r := resolverFor(t, nil, project("p", a, b, down))

	if doc := r.Resolve(down, Existing{}); len(doc.Warnings) != 0 {
		t.Errorf("warnings = %v, want none when the parents agree", doc.Warnings)
	}
}

// A nearer generation overriding a further one is inheritance working, not a
// disagreement, so it is not reported.
func TestGenerationsOverridingEachOtherAreNotAmbiguous(t *testing.T) {
	root := seed("p", "raw", col("id", "The root's wording."))
	mid := node("p", "mid", []string{"seed.p.raw"}, col("id", "The nearer wording."))
	down := node("p", "stg", []string{"mid"}, col("id", ""))
	r := resolverFor(t, nil, project("p", root, mid, down))

	doc := r.Resolve(down, Existing{})
	if got := columnDoc(t, doc, "id"); got.Description != "The nearer wording." {
		t.Errorf("description = %q, want the nearest generation", got.Description)
	}
	if len(doc.Warnings) != 0 {
		t.Errorf("warnings = %v, want none across generations", doc.Warnings)
	}
}

// A column documented locally is not contested: nothing is being decided.
func TestALocallyDocumentedColumnIsNotAmbiguous(t *testing.T) {
	a := seed("p", "customers", col("id", "The customer's identifier."))
	b := seed("p", "orders", col("id", "The order's identifier."))
	down := node("p", "joined", []string{"seed.p.customers", "seed.p.orders"},
		col("id", "Written here, deliberately."))
	r := resolverFor(t, nil, project("p", a, b, down))

	doc := r.Resolve(down, Existing{})
	if got := columnDoc(t, doc, "id"); got.Description != "Written here, deliberately." {
		t.Errorf("description = %q, want the local one kept", got.Description)
	}
	// The ancestors still disagree with one another, and the run still says so:
	// the column is documented today, but the disagreement upstream is real and
	// will decide the answer the moment the local description is removed.
	if len(doc.Warnings) != 1 || doc.Warnings[0].Kind != WarnAmbiguous {
		t.Fatalf("warnings = %v, want the upstream disagreement reported", doc.Warnings)
	}
}

func TestAmbiguityWarningsCanBeTurnedOff(t *testing.T) {
	a := seed("p", "customers", col("id", "The customer's identifier."))
	b := seed("p", "orders", col("id", "The order's identifier."))
	down := node("p", "joined", []string{"seed.p.customers", "seed.p.orders"}, col("id", ""))
	off := false
	r := resolverFor(t, func(c *config.Config) { c.Inheritance.WarnAmbiguous = &off },
		project("p", a, b, down))

	if doc := r.Resolve(down, Existing{}); len(doc.Warnings) != 0 {
		t.Errorf("warnings = %v, want none when turned off", doc.Warnings)
	}
}
