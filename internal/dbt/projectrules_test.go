package dbt_test

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/rognerud/dbt-ditto/internal/dbt"
)

// dbt resolves `+dbt-ditto-path:` into each node's config, so the
// dbt_project.yml rules are only a fallback — but one that never matches is not
// a fallback, hence stripping the project's own package from both sides.
func TestPathRulesFromDbtProjectYAMLMatchTheirNodes(t *testing.T) {
	root := t.TempDir()
	writeProjectFile(t, root, "dbt_project.yml", `name: demo
version: "1.0.0"
config-version: 2
model-paths: ["models"]

models:
  demo:
    +dbt-ditto-path: "_{model}.yml"
    marts:
      +dbt-ditto-path: "_{parent}_models.yml"
`)
	writeProjectFile(t, filepath.Join(root, "target"), "manifest.json", `{
  "metadata": {"project_name": "demo", "dbt_version": "1.8.0"},
  "nodes": {
    "model.demo.stg": {
      "unique_id": "model.demo.stg", "name": "stg", "resource_type": "model",
      "package_name": "demo", "fqn": ["demo", "staging", "stg"],
      "original_file_path": "models/staging/stg.sql", "columns": {}
    },
    "model.demo.fct": {
      "unique_id": "model.demo.fct", "name": "fct", "resource_type": "model",
      "package_name": "demo", "fqn": ["demo", "marts", "fct"],
      "original_file_path": "models/marts/fct.sql", "columns": {}
    }
  },
  "sources": {}
}`)

	p, err := dbt.LoadProject(root, "", true)
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		node string
		want string
	}{
		// The project-level rule applies to a model with no rule of its own.
		{"model.demo.stg", "_{model}.yml"},
		// The more specific folder rule wins where one exists.
		{"model.demo.fct", "_{parent}_models.yml"},
	}
	for _, c := range cases {
		n := p.Manifest.Nodes[c.node]
		if n == nil {
			t.Fatalf("%s is not in the manifest", c.node)
		}
		got, ok := p.PathTemplate(n)
		if !ok {
			t.Errorf("%s: no path template matched, so the dbt_project.yml rules are dead weight", c.node)
			continue
		}
		if got != c.want {
			t.Errorf("%s: template = %q, want %q", c.node, got, c.want)
		}
	}
}

func writeProjectFile(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// Folder tags are read straight out of dbt_project.yml rather than off the
// node's resolved config, so what a folder declares stays distinguishable from
// what a model declared for itself.
func TestFolderTagsFromDbtProjectYAMLAccumulateDownTheTree(t *testing.T) {
	root := t.TempDir()
	writeProjectFile(t, root, "dbt_project.yml", `name: demo
version: "1.0.0"
config-version: 2
model-paths: ["models"]

models:
  demo:
    +tags: ["governed"]
    marts:
      +tags: ["marts"]
      finance:
        +tags: ["data-sensitivity:confidential"]
seeds:
  demo:
    raw:
      tags: loaded
`)
	writeProjectFile(t, filepath.Join(root, "target"), "manifest.json", `{
  "metadata": {"project_name": "demo", "dbt_version": "1.8.0"},
  "nodes": {
    "model.demo.stg": {
      "unique_id": "model.demo.stg", "name": "stg", "resource_type": "model",
      "package_name": "demo", "fqn": ["demo", "staging", "stg"],
      "original_file_path": "models/staging/stg.sql", "columns": {}
    },
    "model.demo.fct_revenue": {
      "unique_id": "model.demo.fct_revenue", "name": "fct_revenue", "resource_type": "model",
      "package_name": "demo", "fqn": ["demo", "marts", "finance", "fct_revenue"],
      "original_file_path": "models/marts/finance/fct_revenue.sql", "columns": {}
    },
    "seed.demo.orders": {
      "unique_id": "seed.demo.orders", "name": "orders", "resource_type": "seed",
      "package_name": "demo", "fqn": ["demo", "raw", "orders"],
      "original_file_path": "seeds/raw/orders.csv", "columns": {}
    }
  },
  "sources": {}
}`)

	p, err := dbt.LoadProject(root, "", true)
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		node string
		want []string
	}{
		// Only the project-wide declaration reaches a model outside marts.
		{"model.demo.stg", []string{"governed"}},
		// dbt accumulates `+tags:` rather than letting the nearest folder win, and
		// the outermost folder comes first so a run writes the same bytes twice.
		{"model.demo.fct_revenue", []string{"governed", "marts", "data-sensitivity:confidential"}},
		// `seeds:` is read too, and the unprefixed spelling with a single tag.
		{"seed.demo.orders", []string{"loaded"}},
	}
	for _, c := range cases {
		n := p.Manifest.Nodes[c.node]
		if n == nil {
			t.Fatalf("manifest has no %s", c.node)
		}
		if got := p.FolderTags(n); !reflect.DeepEqual(got, c.want) {
			t.Errorf("FolderTags(%s) = %v, want %v", c.node, got, c.want)
		}
	}
}

// A project that declares nothing gets no rules, so the feature costs a run
// with no `+tags:` nothing at all.
func TestFolderTagsAreEmptyWithoutDeclarations(t *testing.T) {
	root := t.TempDir()
	writeProjectFile(t, root, "dbt_project.yml", `name: demo
version: "1.0.0"
config-version: 2
models:
  demo:
    +dbt-ditto-path: "_{model}.yml"
`)
	writeProjectFile(t, filepath.Join(root, "target"), "manifest.json", `{
  "metadata": {"project_name": "demo", "dbt_version": "1.8.0"},
  "nodes": {
    "model.demo.stg": {
      "unique_id": "model.demo.stg", "name": "stg", "resource_type": "model",
      "package_name": "demo", "fqn": ["demo", "stg"],
      "original_file_path": "models/stg.sql", "columns": {}
    }
  },
  "sources": {}
}`)

	p, err := dbt.LoadProject(root, "", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.FolderTagRules) != 0 {
		t.Errorf("FolderTagRules = %v, want none", p.FolderTagRules)
	}
	if got := p.FolderTags(p.Manifest.Nodes["model.demo.stg"]); got != nil {
		t.Errorf("FolderTags = %v, want nil", got)
	}
}
