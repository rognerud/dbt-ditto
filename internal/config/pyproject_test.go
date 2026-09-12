package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const pyprojectWithTable = `[project]
name = "analytics"
version = "0.1.0"

[tool.dbt-ditto]
loom = false

[[tool.dbt-ditto.projects]]
path = "projects/platform"

[[tool.dbt-ditto.projects]]
path = "projects/analytics"
upstream = true

[tool.dbt-ditto.inheritance]
progenitor = false
placeholders = ["TODO", "Not documented"]

[tool.dbt-ditto.inheritance.derived]
enabled = true

[tool.dbt-ditto.columns]
order = "alphabetical"
`

// write puts a file in dir and returns its path.
func write(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// The TOML table carries the same keys as the YAML file, including the nested
// sections and the list of projects.
func TestLoadReadsPyprojectTable(t *testing.T) {
	dir := t.TempDir()
	path := write(t, dir, PyprojectFilename, pyprojectWithTable)

	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.Dir != dir {
		t.Errorf("Dir = %q, want %q", c.Dir, dir)
	}
	if len(c.Projects) != 2 {
		t.Fatalf("got %d projects, want 2: %+v", len(c.Projects), c.Projects)
	}
	if c.Projects[0].Path != "projects/platform" {
		t.Errorf("first project = %+v", c.Projects[0])
	}
	if !c.Projects[1].Upstream {
		t.Errorf("second project should be upstream: %+v", c.Projects[1])
	}

	r := c.Resolve()
	if r.Progenitor {
		t.Error("progenitor = true, want the table's false")
	}
	if r.ColumnOrder != "alphabetical" {
		t.Errorf("column order = %q, want alphabetical", r.ColumnOrder)
	}
	if !r.DerivedAggregates {
		t.Error("derived matching did not come through the nested table")
	}
	if r.Placeholders["Undefined"] {
		t.Error("the table's placeholder list did not replace the default one")
	}
	if !r.Placeholders["TODO"] {
		t.Error("the table's own placeholders were not read")
	}
	if c.Loom == nil || *c.Loom {
		t.Error("loom = true, want the table's false")
	}
}

// Underscore and hyphen are the same name to anyone writing it.
func TestPyprojectAcceptsTheUnderscoreSpelling(t *testing.T) {
	dir := t.TempDir()
	path := write(t, dir, PyprojectFilename, `[tool.dbt_ditto]
[[tool.dbt_ditto.projects]]
path = "."
`)

	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Projects) != 1 {
		t.Fatalf("got %d projects, want 1", len(c.Projects))
	}
}

// A pyproject.toml naming no projects is the same mistake as a YAML file naming
// none, and gets the same error.
func TestLoadRejectsPyprojectWithoutTheTable(t *testing.T) {
	dir := t.TempDir()
	path := write(t, dir, PyprojectFilename, "[project]\nname = \"analytics\"\n")

	_, err := Load(path)
	if err == nil {
		t.Fatal("no error for a pyproject.toml that does not configure this tool")
	}
	if !strings.Contains(err.Error(), PyprojectTable) {
		t.Errorf("error = %q, want it to name the missing table", err)
	}
}

// A dedicated config file is a clearer statement of intent than a table in a
// packaging file, so it wins where both are present.
func TestYamlWinsOverPyprojectInTheSameDirectory(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, PyprojectFilename, pyprojectWithTable)
	write(t, dir, "dbt_ditto.yml", "projects:\n  - path: from-yaml\n")

	found, err := discoverFrom(dir)
	if err != nil {
		t.Fatal(err)
	}
	c, err := Load(found)
	if err != nil {
		t.Fatal(err)
	}
	if c.Projects[0].Path != "from-yaml" {
		t.Errorf("loaded %+v, want the YAML file to win", c.Projects[0])
	}
}

// Nearly every Python project has a pyproject.toml and nearly none of them
// configure this tool, so finding one is not by itself an answer: the search has
// to carry on upwards as if it were not there.
func TestDiscoverySkipsAPyprojectWithoutTheTable(t *testing.T) {
	root := t.TempDir()
	write(t, root, "dbt_ditto.yml", "projects:\n  - path: .\n")
	nested := filepath.Join(root, "projects", "analytics")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, nested, PyprojectFilename, "[project]\nname = \"analytics\"\n")

	found, err := discoverFrom(nested)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(found) != "dbt_ditto.yml" {
		t.Errorf("found %q, want the dbt_ditto.yml further up", found)
	}
}

// A pyproject.toml that does carry the table stops the search where it sits.
func TestDiscoveryStopsAtAPyprojectWithTheTable(t *testing.T) {
	root := t.TempDir()
	write(t, root, "dbt_ditto.yml", "projects:\n  - path: .\n")
	nested := filepath.Join(root, "projects", "analytics")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, nested, PyprojectFilename, pyprojectWithTable)

	found, err := discoverFrom(nested)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(found) != nested {
		t.Errorf("found %q, want the nested pyproject.toml", found)
	}
}

// A packaging file this tool cannot parse belongs to someone else's tooling.
// Failing the run on it would make an unrelated syntax error look like ours.
func TestDiscoverySkipsAnUnparseablePyproject(t *testing.T) {
	root := t.TempDir()
	write(t, root, "dbt_ditto.yml", "projects:\n  - path: .\n")
	nested := filepath.Join(root, "sub")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, nested, PyprojectFilename, "this is not = = toml\n")

	found, err := discoverFrom(nested)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(found) != "dbt_ditto.yml" {
		t.Errorf("found %q, want the search to have carried on", found)
	}
}
