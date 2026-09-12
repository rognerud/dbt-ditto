package runner_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rognerud/dbt-ditto/internal/config"
	"github.com/rognerud/dbt-ditto/internal/runner"
)

// dbt-ditto never connects to a warehouse, so covering BigQuery means covering
// the shape of the artifacts dbt-bigquery writes. testdata/bigquery holds a
// manifest and catalog built to match what that adapter actually emits; see the
// README there for where each detail comes from.
//
// The thing that makes BigQuery different from DuckDB, and the reason these
// tests exist: its catalog reports a nested RECORD as the parent column *and*
// every dotted leaf, because the catalog query joins through
// COLUMN_FIELD_PATHS. A tool that expands struct types without checking would
// write every nested field twice.

func bigQueryProject(t *testing.T) string {
	t.Helper()
	src := repoPath(t, "testdata", "bigquery")
	dst := t.TempDir()
	if err := copyTree(src, dst); err != nil {
		t.Fatalf("copy fixture: %v", err)
	}
	return dst
}

func bigQueryConfig(root string) *config.Config {
	return &config.Config{
		Dir:      root,
		Projects: []config.ProjectRef{{Name: "bqshop", Path: root}},
	}
}

// runBigQuery runs over the fixture and returns the written schema files.
func runBigQuery(t *testing.T, tweak func(*config.Config)) map[string]string {
	t.Helper()
	root := bigQueryProject(t)
	cfg := bigQueryConfig(root)
	if tweak != nil {
		tweak(cfg)
	}
	if _, err := runner.Run(cfg, runner.Options{Organize: true}); err != nil {
		t.Fatalf("run: %v", err)
	}
	return snapshotTree(t, root)
}

// countEntries counts how many times a column is listed, which is the whole
// point: on BigQuery a nested field is easy to write twice.
func countEntries(doc, column string) int {
	return strings.Count(doc, "- name: "+column+"\n")
}

func TestBigQueryNestedFieldsAreNotDuplicated(t *testing.T) {
	files := runBigQuery(t, nil)
	doc := files["models/staging/_stg_customers.yml"]
	if doc == "" {
		t.Fatal("stg_customers was not written")
	}

	for _, column := range []string{
		"customer", "customer.first_name", "customer.last_name",
		"customer.address", "customer.address.city", "customer.address.country",
		"items", "items.sku", "items.quantity",
	} {
		if got := countEntries(doc, column); got != 1 {
			t.Errorf("%s appears %d times, want exactly 1:\n%s", column, got, doc)
		}
	}
}

// Everything the catalog reports has to be written, including the doubly
// nested fields and the fields of a repeated record.
func TestBigQueryEveryCatalogColumnIsWritten(t *testing.T) {
	files := runBigQuery(t, nil)
	doc := files["models/staging/_stg_customers.yml"]

	want := map[string]string{
		"customer_id":              "INT64",
		"email":                    "STRING",
		"customer.address.country": "STRING",
		"items.quantity":           "INT64",
		"lifetime_value":           "NUMERIC(38, 9)",
		"signed_up_at":             "TIMESTAMP",
		"home_location":            "GEOGRAPHY",
		"raw_payload":              "JSON",
	}
	for column, dataType := range want {
		if countEntries(doc, column) != 1 {
			t.Errorf("%s is missing:\n%s", column, doc)
			continue
		}
		if !strings.Contains(doc, "data_type: "+dataType) {
			t.Errorf("%s should carry data_type %s:\n%s", column, dataType, doc)
		}
	}
}

// A NUMERIC with precision and scale contains a comma, which must not be read
// as a field separator, and an ARRAY<STRUCT<...>> must survive intact.
func TestBigQueryCompositeTypesAreWrittenVerbatim(t *testing.T) {
	doc := runBigQuery(t, nil)["models/staging/_stg_customers.yml"]

	for _, dataType := range []string{
		"NUMERIC(38, 9)",
		"ARRAY<STRUCT<`sku` STRING, `quantity` INT64>>",
		"STRUCT<`city` STRING, `country` STRING>",
	} {
		if !strings.Contains(doc, dataType) {
			t.Errorf("type %q was not written verbatim:\n%s", dataType, doc)
		}
	}
}

// A column key dbt-ditto does not manage, such as BigQuery's policy_tags, must
// survive a rewrite untouched.
func TestBigQueryPolicyTagsSurvive(t *testing.T) {
	doc := runBigQuery(t, nil)["models/staging/_stg_customers.yml"]

	if !strings.Contains(doc, "policy_tags:") {
		t.Errorf("policy_tags was dropped:\n%s", doc)
	}
	if !strings.Contains(doc, "projects/bq-project/locations/eu/taxonomies/1/policyTags/2") {
		t.Errorf("the policy tag value was lost:\n%s", doc)
	}
}

// dbt 1.10 means meta belongs under `config:`, decided from the manifest's own
// version rather than from anything the user has to configure.
func TestBigQueryUsesTheConfigBlock(t *testing.T) {
	doc := runBigQuery(t, nil)["models/staging/_stg_customers.yml"]

	if !strings.Contains(doc, "config:") || !strings.Contains(doc, "owner: platform-team") {
		t.Fatalf("meta was not written:\n%s", doc)
	}
	if strings.Index(doc, "config:") > strings.Index(doc, "owner: platform-team") {
		t.Errorf("meta should be nested under config: for dbt 1.10:\n%s", doc)
	}
}

// Nested documentation flows down to a model that keeps the same nesting.
func TestBigQueryNestedDocumentationIsInherited(t *testing.T) {
	doc := runBigQuery(t, nil)["models/marts/_dim_customers.yml"]
	if doc == "" {
		t.Fatal("dim_customers was not written")
	}
	if !strings.Contains(doc, "Customer given name, as typed by the customer.") {
		t.Errorf("customer.first_name did not inherit:\n%s", doc)
	}
	if !strings.Contains(doc, "Lifetime gross revenue from the customer.") {
		t.Errorf("lifetime_value did not inherit:\n%s", doc)
	}
}

// Unnesting a BigQuery record into flat columns, which needs derived matching.
func TestBigQueryUnnestedColumnsInheritWithDerivedMatching(t *testing.T) {
	doc := runBigQuery(t, func(c *config.Config) {
		on := true
		c.Inheritance.Derived.Enabled = &on
	})["models/marts/_dim_customers.yml"]

	// `first_name` here came from `customer.first_name` upstream.
	entry := columnEntry(t, doc, "first_name")
	if !strings.Contains(entry, "Customer given name, as typed by the customer.") {
		t.Errorf("first_name did not inherit from customer.first_name:\n%s", doc)
	}
	// `city` came from `customer.address.city`, two levels deep.
	entry = columnEntry(t, doc, "city")
	if !strings.Contains(entry, "City of the customer's billing address.") {
		t.Errorf("city did not inherit from customer.address.city:\n%s", doc)
	}
}

// BigQuery aggregate names, with derived matching on.
func TestBigQueryAggregatesInheritWithDerivedMatching(t *testing.T) {
	doc := runBigQuery(t, func(c *config.Config) {
		on := true
		c.Inheritance.Derived.Enabled = &on
	})["models/marts/_fct_customer_totals.yml"]

	if !strings.Contains(doc, "Lifetime gross revenue from the customer.") {
		t.Errorf("total_lifetime_value did not inherit from lifetime_value:\n%s", doc)
	}
}

// A BigQuery source documented only in the warehouse: `description` on a
// column and on a nested field path both arrive as catalog comments.
func TestBigQuerySourceIsDocumentedFromWarehouseDescriptions(t *testing.T) {
	doc := runBigQuery(t, nil)["models/staging/_sources.yml"]
	if doc == "" {
		t.Fatal("the sources file was not written")
	}

	if !strings.Contains(doc, "Surrogate key assigned by the CRM.") {
		t.Errorf("the warehouse description for customer_id was not used:\n%s", doc)
	}
	if !strings.Contains(doc, "Given name exactly as the CRM stores it.") {
		t.Errorf("the warehouse description for customer.first_name was not used:\n%s", doc)
	}
	if countEntries(doc, "customer.first_name") != 1 {
		t.Errorf("customer.first_name is duplicated in the source:\n%s", doc)
	}
}

// And the rest of that source, filled in from the staging model below it.
func TestBigQuerySourceIsBackfilledFromTheStagingModel(t *testing.T) {
	doc := runBigQuery(t, func(c *config.Config) {
		on := true
		c.Inheritance.Backfill.Enabled = &on
	})["models/staging/_sources.yml"]

	if !strings.Contains(doc, "Customer contact address, restricted.") {
		t.Errorf("email was not backfilled from stg_customers:\n%s", doc)
	}
	if !strings.Contains(doc, "Lifetime gross revenue from the customer.") {
		t.Errorf("lifetime_value was not backfilled:\n%s", doc)
	}
}

// dbt-osmosis rebuilds each column entry and always ends it with the config
// block. A column that already had a config block and is now gaining a
// data_type must not end up with the two the other way round.
func TestBigQueryConfigBlockStaysLast(t *testing.T) {
	doc := runBigQuery(t, nil)["models/staging/_stg_customers.yml"]

	// customer_id starts out with a config block and no data_type.
	entry := columnEntry(t, doc, "customer_id")
	config, dataType := strings.Index(entry, "config:"), strings.Index(entry, "data_type:")
	if config < 0 || dataType < 0 {
		t.Fatalf("expected both keys on customer_id:\n%s", entry)
	}
	if dataType > config {
		t.Errorf("data_type should come before the config block:\n%s", entry)
	}
}

// A second run must change nothing, which on BigQuery is the real test that
// nested fields are handled consistently: an unstable expansion would add a
// duplicate on every run.
func TestBigQueryRunIsIdempotent(t *testing.T) {
	root := bigQueryProject(t)
	cfg := bigQueryConfig(root)

	first, err := runner.Run(cfg, runner.Options{Organize: true})
	if err != nil {
		t.Fatalf("first run: %v", err)
	}
	if len(first.FilesWritten) == 0 {
		t.Fatal("the first run wrote nothing, so this proves nothing")
	}

	second, err := runner.Run(cfg, runner.Options{Organize: true})
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if len(second.FilesWritten) != 0 || len(second.FilesDeleted) != 0 {
		t.Errorf("second run was not a no-op: wrote %v, deleted %v",
			second.FilesWritten, second.FilesDeleted)
	}
}

// columnEntry returns just the YAML block for one column, so an assertion
// cannot accidentally match text belonging to a different column.
func columnEntry(t *testing.T, doc, column string) string {
	t.Helper()
	marker := "- name: " + column + "\n"
	start := strings.Index(doc, marker)
	if start < 0 {
		t.Fatalf("column %s not found in:\n%s", column, doc)
	}
	rest := doc[start+len(marker):]
	if end := strings.Index(rest, "- name: "); end >= 0 {
		return rest[:end]
	}
	return rest
}

// The fixture has to stay in step with the adapter it imitates.
func TestBigQueryFixtureIsPresent(t *testing.T) {
	for _, name := range []string{
		"target/manifest.json", "target/catalog.json", "dbt_project.yml", "README.md",
	} {
		path := repoPath(t, "testdata", "bigquery", name)
		if _, err := os.Stat(path); err != nil {
			t.Errorf("%s: %v", filepath.Base(name), err)
		}
	}
}
