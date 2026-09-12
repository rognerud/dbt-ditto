package sources

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rognerud/dbt-ditto/internal/config"
	"github.com/rognerud/dbt-ditto/internal/dbt"
)

func customers() RequestSource {
	return RequestSource{
		UniqueID: "source.shop.crm.customers",
		Database: "bq-project", Schema: "Raw", Identifier: "customers",
	}
}

func TestProviderClaimsMatchOnGlobsAndIgnoreCase(t *testing.T) {
	cases := []struct {
		name     string
		provider Provider
		want     bool
	}{
		{"no patterns claims everything", Provider{}, true},
		{"database glob", Provider{Database: "bq-*"}, true},
		{"database mismatch", Provider{Database: "sf-*"}, false},
		// Warehouse identifiers are not case sensitive in most places, and a
		// pattern that matched only one spelling would be a trap.
		{"schema folded", Provider{Schema: "raw"}, true},
		{"both must match", Provider{Database: "bq-*", Schema: "staging"}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.provider.Claims(customers()); got != c.want {
				t.Fatalf("Claims = %v, want %v", got, c.want)
			}
		})
	}
}

func TestFetchReadsTheRequestAndDecodesTheAnswer(t *testing.T) {
	// The provider proves it was given the request by echoing a unique_id it
	// could not otherwise know.
	p := Provider{Command: `cat >/dev/null; printf '%s' '{"version":1,"sources":[{"unique_id":"source.shop.crm.customers","description":"From the CRM."}]}'`}

	set := Fetch(context.Background(), []Provider{p}, nil, []RequestSource{customers()})
	doc, ok := set.Lookup("source.shop.crm.customers")
	if !ok {
		t.Fatalf("nothing was decoded, warnings: %v", set.Warnings)
	}
	if doc.Description != "From the CRM." {
		t.Fatalf("description = %q", doc.Description)
	}
}

func TestAProviderThatFailsIsAWarningNotAnError(t *testing.T) {
	// A tool that tidies YAML should still tidy it when the warehouse is
	// unreachable. The last line of stderr is carried through, because a Python
	// traceback is twenty lines of noise and one line of explanation.
	bad := Provider{Command: `cat >/dev/null; echo "Traceback (most recent call last):" >&2; echo "PermissionDenied: no access" >&2; exit 1`}
	good := Provider{Command: `cat >/dev/null; printf '%s' '{"version":1,"sources":[{"unique_id":"source.shop.crm.customers"}]}'`}

	set := Fetch(context.Background(), []Provider{bad, good}, nil, []RequestSource{customers()})
	if _, ok := set.Lookup("source.shop.crm.customers"); !ok {
		t.Error("one provider failing should not stop the others contributing")
	}
	if len(set.Warnings) != 1 {
		t.Fatalf("warnings = %v, want one naming the failure", set.Warnings)
	}
	if want := "PermissionDenied: no access"; !contains(set.Warnings[0], want) {
		t.Errorf("warning = %q, want it to carry %q", set.Warnings[0], want)
	}
}

func TestTheFirstProviderToClaimASourceWins(t *testing.T) {
	first := Provider{Command: `cat >/dev/null; printf '%s' '{"version":1,"sources":[{"unique_id":"source.shop.crm.customers","description":"first"}]}'`}
	second := Provider{Command: `cat >/dev/null; printf '%s' '{"version":1,"sources":[{"unique_id":"source.shop.crm.customers","description":"second"}]}'`}

	set := Fetch(context.Background(), []Provider{first, second}, nil, []RequestSource{customers()})
	doc, _ := set.Lookup("source.shop.crm.customers")
	if doc.Description != "first" {
		t.Errorf("description = %q, want the first configured provider's", doc.Description)
	}
	if len(set.Warnings) != 1 {
		t.Errorf("two answers for one source should be reported, got %v", set.Warnings)
	}
}

func TestAProviderSpeakingANewerContractIsRefused(t *testing.T) {
	// Guessing at a format this build does not know is worse than declining.
	p := Provider{Command: `cat >/dev/null; printf '%s' '{"version":99,"sources":[]}'`}
	set := Fetch(context.Background(), []Provider{p}, nil, []RequestSource{customers()})
	if len(set.Warnings) != 1 || !contains(set.Warnings[0], "contract version 99") {
		t.Fatalf("warnings = %v, want one naming the version", set.Warnings)
	}
}

func TestCacheRoundTrips(t *testing.T) {
	path := filepath.Join(t.TempDir(), "target", "ditto-sources.json")
	in := &Set{Docs: map[string]*Doc{
		"source.shop.crm.customers": {
			UniqueID:    "source.shop.crm.customers",
			Description: "From the CRM.",
			Labels:      map[string]string{"owner": "crm"},
			Columns:     []ColumnDoc{{Name: "id", DataType: "INT64", Index: 1}},
		},
	}}
	if err := Save(path, in); err != nil {
		t.Fatal(err)
	}
	out, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	doc, ok := out.Lookup("source.shop.crm.customers")
	if !ok || doc.Description != "From the CRM." || doc.Columns[0].DataType != "INT64" {
		t.Fatalf("round trip lost something: %+v", doc)
	}
}

// A missing cache means no enrichment, exactly as a missing catalog.json means
// no column reconciliation. That is what lets --check run on a machine with no
// credentials and no provider installed.
func TestAMissingCacheIsNotAnError(t *testing.T) {
	set, err := Load(filepath.Join(t.TempDir(), "nothing.json"))
	if err != nil {
		t.Fatalf("a missing cache should be silence, got %v", err)
	}
	if set.Len() != 0 {
		t.Fatalf("expected an empty set, got %d", set.Len())
	}
}

func TestApplyWritesACatalogEntryForTheSource(t *testing.T) {
	// Provider output becomes a catalog, because a catalog is already the thing
	// that says what a relation really contains — so column injection, stale
	// removal, ordering and struct expansion all keep working unchanged.
	p := &dbt.Project{Name: "shop", Manifest: &dbt.Manifest{
		Nodes: map[string]*dbt.Node{}, Sources: map[string]*dbt.Node{},
	}}
	n := &dbt.Node{
		UniqueID: "source.shop.crm.customers", Name: "customers",
		ResourceType: "source", Database: "bq", Schema: "raw", Identifier: "customers",
		Project: p, Columns: map[string]*dbt.Column{},
	}

	set := &Set{Docs: map[string]*Doc{n.UniqueID: {
		UniqueID:    n.UniqueID,
		Description: "Customer master.",
		Columns: []ColumnDoc{
			{Name: "id", DataType: "INT64", Description: "The key.", Index: 1},
			{Name: "email", DataType: "STRING", Index: 2},
		},
	}}}

	if w := Apply(set, []*dbt.Node{n}, (&config.Config{}).Resolve()); len(w) != 0 {
		t.Fatalf("unexpected warnings: %v", w)
	}

	if n.Description != "Customer master." {
		t.Errorf("node description = %q", n.Description)
	}
	entry, ok := p.Catalog.Lookup(n)
	if !ok {
		t.Fatal("no catalog entry was written, so nothing knows the relation's columns")
	}
	if len(entry.Columns) != 2 || entry.Columns["id"].Type != "INT64" {
		t.Fatalf("catalog entry = %+v", entry.Columns)
	}
	// The column now exists in the manifest too, so inheritance downstream can
	// see it at all.
	if c := n.Column("id", true); c == nil || c.Description != "The key." {
		t.Fatalf("manifest column = %+v", c)
	}
}

func TestApplyNeverOverwritesSomethingWrittenByHand(t *testing.T) {
	p := &dbt.Project{Name: "shop", Manifest: &dbt.Manifest{
		Nodes: map[string]*dbt.Node{}, Sources: map[string]*dbt.Node{},
	}}
	n := &dbt.Node{
		UniqueID: "source.shop.crm.customers", Name: "customers",
		ResourceType: "source", Database: "bq", Schema: "raw", Identifier: "customers",
		Project:     p,
		Description: "What the analyst decided it means.",
		Columns: map[string]*dbt.Column{
			"id": {Name: "id", Description: "Reviewed by a person."},
		},
	}
	set := &Set{Docs: map[string]*Doc{n.UniqueID: {
		UniqueID:    n.UniqueID,
		Description: "Whatever the warehouse says.",
		Columns:     []ColumnDoc{{Name: "id", Description: "Whatever the warehouse says."}},
	}}}

	Apply(set, []*dbt.Node{n}, (&config.Config{}).Resolve())

	if n.Description != "What the analyst decided it means." {
		t.Errorf("node description was overwritten: %q", n.Description)
	}
	if got := n.Column("id", true).Description; got != "Reviewed by a person." {
		t.Errorf("column description was overwritten: %q", got)
	}
}

func TestLabelsAreFilteredBeforeTheyAreRouted(t *testing.T) {
	l := config.ResolvedLabels{Mode: config.LabelsMeta, Exclude: []string{"terraform_*"}}
	kept, bad := filterLabels(map[string]string{
		"owner":           "crm",
		"terraform_stack": "infra",
	}, l)
	if len(bad) != 0 {
		t.Fatalf("unexpected complaints: %v", bad)
	}
	if _, dropped := kept["terraform_stack"]; dropped {
		t.Error("an excluded key was kept")
	}
	if kept["owner"] != "crm" {
		t.Error("an unexcluded key was dropped")
	}
}

// BigQuery permits a label with no value, and `owner:` is not a useful tag.
func TestAValuelessLabelRendersAsTheBareKey(t *testing.T) {
	l := config.ResolvedLabels{TagFormat: config.DefaultTagFormat}
	got := RenderTags(map[string]string{"pii": "high", "reviewed": ""}, l)
	want := []string{"pii:high", "reviewed"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("tags = %v, want %v", got, want)
	}
}

func contains(haystack, needle string) bool { return strings.Contains(haystack, needle) }
