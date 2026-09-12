package canon

import (
	"os"
	"path/filepath"
	"testing"
)

// dbt's node order is not deterministic, so the same documentation can arrive
// as two different files; canonicalising both has to produce the same bytes.
func TestTwoOrdersCanoniciseAlike(t *testing.T) {
	const first = `version: 2
models:
  - name: dim_regions
    columns:
      - name: region_code
        data_type: VARCHAR
  - name: dim_customers
    description: Customers.
    columns:
      - name: customer_id
        data_type: INTEGER
`
	const second = `version: 2
models:
  - name: dim_customers
    description: Customers.
    columns:
      - name: customer_id
        data_type: INTEGER
  - name: dim_regions
    columns:
      - name: region_code
        data_type: VARCHAR
`
	a := write(t, "a.yml", first)
	b := write(t, "b.yml", second)

	if err := File(a); err != nil {
		t.Fatalf("canon %s: %v", a, err)
	}
	if err := File(b); err != nil {
		t.Fatalf("canon %s: %v", b, err)
	}
	if got, want := read(t, a), read(t, b); got != want {
		t.Fatalf("the two orders did not converge:\n--- a\n%s\n--- b\n%s", got, want)
	}
	// Sorted, and both entries still carry what they carried.
	if got := read(t, a); got != second {
		t.Errorf("unexpected canonical form:\n%s", got)
	}
}

// Column order inside an entry is the warehouse's, which both tools reproduce.
func TestColumnOrderIsKept(t *testing.T) {
	const in = `version: 2
models:
  - name: stg_orders
    columns:
      - name: order_id
      - name: customer_id
      - name: amount_cents
`
	p := write(t, "cols.yml", in)
	if err := File(p); err != nil {
		t.Fatalf("canon: %v", err)
	}
	if got := read(t, p); got != in {
		t.Errorf("column order changed:\n%s", got)
	}
}

// Sources nest one level deeper: sources and their tables are both named.
func TestSourceTablesAreSorted(t *testing.T) {
	const in = `version: 2
sources:
  - name: raw
    tables:
      - name: orders
      - name: customers
`
	const want = `version: 2
sources:
  - name: raw
    tables:
      - name: customers
      - name: orders
`
	p := write(t, "sources.yml", in)
	if err := File(p); err != nil {
		t.Fatalf("canon: %v", err)
	}
	if got := read(t, p); got != want {
		t.Errorf("source tables were not sorted:\n%s", got)
	}
}

// Configuration files are not schema: their top-level keys are not entry lists.
func TestConfigFilesAreSkipped(t *testing.T) {
	for _, name := range []string{"dbt_project.yml", "profiles.yml", "dbt_ditto.yml"} {
		if IsSchemaFile(name) {
			t.Errorf("%s was treated as schema YAML", name)
		}
	}
	for _, name := range []string{"_models.yml", "schema.yaml"} {
		if !IsSchemaFile(name) {
			t.Errorf("%s was not treated as schema YAML", name)
		}
	}
}

// Dir walks a project, and leaves generated directories alone.
func TestDirSkipsGeneratedDirectories(t *testing.T) {
	root := t.TempDir()
	const unsorted = "version: 2\nmodels:\n  - name: b\n  - name: a\n"
	mkdir(t, filepath.Join(root, "models"))
	mkdir(t, filepath.Join(root, "target"))
	schema := filepath.Join(root, "models", "_schema.yml")
	generated := filepath.Join(root, "target", "_schema.yml")
	writeTo(t, schema, unsorted)
	writeTo(t, generated, unsorted)

	if err := Dir(root); err != nil {
		t.Fatalf("Dir: %v", err)
	}
	if got, want := read(t, schema), "version: 2\nmodels:\n  - name: a\n  - name: b\n"; got != want {
		t.Errorf("the schema file was not canonicalised:\n%s", got)
	}
	if got := read(t, generated); got != unsorted {
		t.Errorf("a file under target/ was rewritten:\n%s", got)
	}
}

func write(t *testing.T, name, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	writeTo(t, p, body)
	return p
}

func writeTo(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func mkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(raw)
}
