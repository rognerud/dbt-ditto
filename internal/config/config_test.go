package config

import (
	"os"
	"path/filepath"
	"testing"
)

// The defaults are a promise: pointing dbt-ditto at a project dbt-osmosis
// manages must not change the YAML, beyond the progenitor annotation below.
func TestDefaultsMatchDbtOsmosis(t *testing.T) {
	r := (&Config{}).Resolve()

	checks := []struct {
		name string
		got  any
		want any
	}{
		{"inherit columns", r.InheritColumns, true},
		{"inherit node descriptions", r.InheritNodeDescription, false},
		{"inherit meta", r.InheritMeta, true},
		{"inherit tags", r.InheritTags, true},
		{"case insensitive matching", r.CaseInsensitive, true},
		{"force", r.Force, false},
		// The one deliberate departure: provenance is recorded by default.
		{"progenitor annotation", r.Progenitor, true},
		{"progenitor key", r.ProgenitorKey, "osmosis_progenitor"},
		{"directives", r.Directives, true},
		{"directive prefix", r.DirectivePrefix, DefaultDirectivePrefix},
		{"ambiguity warnings", r.WarnAmbiguous, true},
		// Off: it writes meta dbt-osmosis never would, breaking byte parity.
		{"ambiguity annotation", r.AmbiguityMeta, false},
		{"ambiguity key", r.AmbiguityKey, DefaultAmbiguityKey},
		{"add missing columns", r.AddMissing, true},
		{"remove stale columns", r.RemoveStale, true},
		{"write data types", r.DataTypes, true},
		{"column case", r.ColumnCase, "preserve"},
		{"column order", r.ColumnOrder, OrderCatalog},
		{"organize", r.Organize, true},
		{"delete emptied files", r.DeleteEmpty, true},
		{"comment handling", r.Comments, CommentsFollow},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %v, want %v", c.name, c.got, c.want)
		}
	}
}

// dbt-osmosis compares placeholders exactly: no trimming, no case folding.
func TestPlaceholderMatchingIsExact(t *testing.T) {
	r := (&Config{}).Resolve()

	for _, want := range DefaultPlaceholders {
		if !r.IsPlaceholder(want) {
			t.Errorf("%q should be a placeholder", want)
		}
	}
	for _, notPlaceholder := range []string{"not documented", "  Not documented  ", "TODO", "A real description."} {
		if r.IsPlaceholder(notPlaceholder) {
			t.Errorf("%q should not be a placeholder", notPlaceholder)
		}
	}
}

func TestCustomPlaceholdersStillIncludeTheEmptyString(t *testing.T) {
	c := &Config{}
	c.Inheritance.Placeholders = []string{"TODO"}
	r := c.Resolve()

	if !r.IsPlaceholder("TODO") {
		t.Error("the configured placeholder was not honoured")
	}
	if !r.IsPlaceholder("") {
		t.Error("an empty description is always a placeholder")
	}
	if r.IsPlaceholder("Not documented") {
		t.Error("configuring placeholders should replace the defaults, not add to them")
	}
}

func TestLoadReadsAProjectList(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "dbt_ditto.yml")
	doc := `
projects:
  - name: platform
    path: projects/platform
  - name: vendor
    path: ../vendor/dbt
    upstream: true
inheritance:
  node_description: true
  skip_meta_keys: [owner]
columns:
  data_types: false
output:
  comments: osmosis
`
	if err := os.WriteFile(path, []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}

	c, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if c.Dir != dir {
		t.Errorf("dir = %q, want %q: paths are resolved against the config file", c.Dir, dir)
	}
	if len(c.Projects) != 2 || !c.Projects[1].Upstream {
		t.Fatalf("projects = %+v", c.Projects)
	}

	r := c.Resolve()
	if !r.InheritNodeDescription {
		t.Error("node_description was not applied")
	}
	if r.DataTypes {
		t.Error("data_types: false was not applied")
	}
	if !r.SkipMetaKeys["owner"] {
		t.Error("skip_meta_keys was not applied")
	}
	if r.Comments != CommentsOsmosis {
		t.Errorf("comments = %q, want %q", r.Comments, CommentsOsmosis)
	}
}

func TestLoadRejectsAConfigWithNoProjects(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "dbt_ditto.yml")
	if err := os.WriteFile(path, []byte("inheritance:\n  columns: true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("expected an error when no projects are configured")
	}
}

func TestDefaultBuildsASingleProjectConfig(t *testing.T) {
	c := Default("some/project")
	if len(c.Projects) != 1 {
		t.Fatalf("projects = %+v", c.Projects)
	}
	if !filepath.IsAbs(c.Projects[0].Path) {
		t.Errorf("path = %q, want it made absolute", c.Projects[0].Path)
	}
}
