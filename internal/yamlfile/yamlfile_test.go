package yamlfile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTemp(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "schema.yml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// Anything dbt-ditto does not manage — comments, key order, tests, unknown keys
// — has to survive untouched, or nobody will run this on their repository.
func TestUnmanagedContentSurvivesARoundTrip(t *testing.T) {
	const doc = `version: 2

# A file-level note.
models:
  - name: stg_orders
    description: Order lines.
    # A note about the columns.
    columns:
      - name: order_id  # inline note
        description: The key.
        tests:
          - unique
          - not_null
        some_unknown_key:
          nested: value
`
	path := writeTemp(t, doc)
	f, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	out, err := f.Render()
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	got := string(out)

	for _, want := range []string{
		"# A file-level note.",
		"# A note about the columns.",
		"# inline note",
		"- unique",
		"some_unknown_key:",
		"nested: value",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("round trip lost %q:\n%s", want, got)
		}
	}
}

func TestSaveOnlyWritesWhenSomethingChanged(t *testing.T) {
	path := writeTemp(t, "version: 2\nmodels:\n  - name: a\n")
	f, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}

	// Loading and saving without edits must leave the file alone.
	if wrote, err := f.Save(false); err != nil {
		t.Fatal(err)
	} else if wrote {
		t.Error("an untouched file was rewritten")
	}

	entry := f.Entry("models", "a", false)
	MapSet(entry, "description", Scalar("Now documented."))
	if wrote, err := f.Save(false); err != nil {
		t.Fatal(err)
	} else if !wrote {
		t.Error("an edited file was not written")
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "Now documented.") {
		t.Errorf("file on disk = %q", data)
	}
}

func TestDryRunReportsWithoutWriting(t *testing.T) {
	path := writeTemp(t, "version: 2\nmodels:\n  - name: a\n")
	f, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	MapSet(f.Entry("models", "a", false), "description", Scalar("Changed."))

	wrote, err := f.Save(true)
	if err != nil {
		t.Fatal(err)
	}
	if !wrote {
		t.Error("dry run should still report that a write is needed")
	}
	data, _ := os.ReadFile(path)
	if strings.Contains(string(data), "Changed.") {
		t.Error("dry run wrote to disk")
	}
}

func TestEntryCreatesAndFinds(t *testing.T) {
	f := New(filepath.Join(t.TempDir(), "schema.yml"))

	if got := f.Entry("models", "absent", false); got != nil {
		t.Error("found an entry that was never created")
	}
	created := f.Entry("models", "stg_orders", true)
	if created == nil {
		t.Fatal("entry was not created")
	}
	if again := f.Entry("models", "stg_orders", true); again != created {
		t.Error("a second call created a duplicate entry")
	}
	if StringOf(MapGet(created, "name")) != "stg_orders" {
		t.Error("the created entry has no name")
	}
}

func TestRemoveEntryDropsTheSectionWhenItEmpties(t *testing.T) {
	path := writeTemp(t, "version: 2\nmodels:\n  - name: a\n  - name: b\n")
	f, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}

	if !f.RemoveEntry("models", "a") {
		t.Fatal("removing a present entry reported false")
	}
	if f.RemoveEntry("models", "a") {
		t.Error("removing an absent entry reported true")
	}
	if f.IsEmpty() {
		t.Error("the file still holds an entry and is not empty")
	}

	f.RemoveEntry("models", "b")
	if MapGet(f.Root(), "models") != nil {
		t.Error("the empty models section should have been dropped")
	}
	if !f.IsEmpty() {
		t.Error("a file holding only `version` is empty and may be deleted")
	}
}

func TestSourceTableNavigatesTheNesting(t *testing.T) {
	f := New(filepath.Join(t.TempDir(), "schema.yml"))

	if got := f.SourceTable("crm", "raw_orders", false); got != nil {
		t.Error("found a source table that does not exist")
	}
	table := f.SourceTable("crm", "raw_orders", true)
	if table == nil {
		t.Fatal("source table was not created")
	}
	MapSet(table, "description", Scalar("Order lines."))

	out, err := f.Render()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"sources:", "name: crm", "tables:", "name: raw_orders", "Order lines."} {
		if !strings.Contains(string(out), want) {
			t.Errorf("rendered file is missing %q:\n%s", want, out)
		}
	}
}

// Replacing a value in place keeps the key where it was, comment included.
func TestMapSetPreservesPositionAndComments(t *testing.T) {
	path := writeTemp(t, "version: 2\nmodels:\n  - name: a\n    # why this matters\n    description: old\n    meta:\n      owner: x\n")
	f, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	entry := f.Entry("models", "a", false)
	MapSet(entry, "description", Scalar("new"))

	out, err := f.Render()
	if err != nil {
		t.Fatal(err)
	}
	got := string(out)
	if !strings.Contains(got, "# why this matters") {
		t.Errorf("the comment on the replaced key was lost:\n%s", got)
	}
	if strings.Index(got, "description: new") > strings.Index(got, "owner: x") {
		t.Errorf("the replaced key moved to the end:\n%s", got)
	}
}

func TestMapDelete(t *testing.T) {
	f := New(filepath.Join(t.TempDir(), "schema.yml"))
	entry := f.Entry("models", "a", true)
	MapSet(entry, "description", Scalar("x"))

	if !MapDelete(entry, "description") {
		t.Error("deleting a present key reported false")
	}
	if MapDelete(entry, "description") {
		t.Error("deleting an absent key reported true")
	}
	if MapGet(entry, "description") != nil {
		t.Error("the key is still there")
	}
}

// A multi-line description is written as a literal block, not one escaped line.
func TestMultiLineScalarsUseALiteralBlock(t *testing.T) {
	f := New(filepath.Join(t.TempDir(), "schema.yml"))
	MapSet(f.Entry("models", "a", true), "description", Scalar("First line.\nSecond line.\n"))

	out, err := f.Render()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "description: |") {
		t.Errorf("want a literal block:\n%s", out)
	}
}

func TestLoadTreatsAnEmptyFileAsANewDocument(t *testing.T) {
	path := writeTemp(t, "   \n\n")
	f, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !f.IsEmpty() {
		t.Error("an empty file should start out empty")
	}
	if MapGet(f.Root(), "version") == nil {
		t.Error("a fresh document should carry `version: 2`")
	}
}

func TestLoadRejectsAListAtTheTopLevel(t *testing.T) {
	path := writeTemp(t, "- not\n- a\n- mapping\n")
	if _, err := Load(path); err == nil {
		t.Fatal("expected an error for a top-level list")
	}
}

func TestLoadOrNewCreatesAMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "schema.yml")
	f, err := LoadOrNew(path)
	if err != nil {
		t.Fatalf("load or new: %v", err)
	}
	if !f.Created {
		t.Error("the file should be reported as newly created")
	}

	MapSet(f.Entry("models", "a", true), "description", Scalar("x"))
	if _, err := f.Save(false); err != nil {
		t.Fatalf("save: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("saving should have created the parent directory: %v", err)
	}
}
