package features

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// upstream is a second dbt project beside the scenario's own: another
// repository's checkout, or the artifact a dbt-loom setup hands over. It is
// written once, when it is declared, and read again after the run to prove
// nothing wrote to it.
type upstream struct {
	name  string
	dir   string
	files map[string]string
}

// anUpstreamProject builds a complete project of its own — dbt_project.yml, a
// schema file and its artifacts — documenting one column of one public model.
// That is the whole of what a downstream project needs from it.
func (w *world) anUpstreamProject(name, model, column, description string) error {
	if w.dir == "" {
		return fmt.Errorf("declare the project first: an upstream is beside it")
	}
	u := &upstream{
		name:  name,
		dir:   filepath.Join(filepath.Dir(w.dir), name),
		files: map[string]string{},
	}
	id := "model." + name + "." + model
	schema := "models/_" + model + ".yml"

	u.files["dbt_project.yml"] = "name: " + name + "\nversion: \"1.0.0\"\nconfig-version: 2\n" +
		"profile: " + name + "\nmodel-paths: [\"models\"]\n"
	u.files[schema] = fmt.Sprintf("version: 2\nmodels:\n  - name: %s\n    columns:\n      - name: %s\n        description: %s\n",
		model, column, description)

	node := map[string]any{
		"unique_id":          id,
		"name":               model,
		"resource_type":      "model",
		"package_name":       name,
		"schema":             "main",
		"database":           name,
		"original_file_path": "models/" + model + ".sql",
		"path":               model + ".sql",
		"patch_path":         name + "://" + schema,
		"fqn":                []string{name, model},
		"columns": map[string]any{
			column: map[string]any{"name": column, "description": description},
		},
		"depends_on": map[string]any{"nodes": []string{}},
		"access":     "public",
		"config":     map[string]any{"dbt-osmosis": "_{model}.yml"},
	}
	manifest, err := json.Marshal(map[string]any{
		"metadata": map[string]string{"project_name": name, "dbt_version": "1.8.0", "adapter_type": "duckdb"},
		"nodes":    map[string]any{id: node},
		"sources":  map[string]any{},
	})
	if err != nil {
		return err
	}
	u.files["target/manifest.json"] = string(manifest)
	u.files["target/catalog.json"] = `{"metadata":{"dbt_version":"1.8.0"},"nodes":{},"sources":{}}`

	for rel, body := range u.files {
		path := filepath.Join(u.dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			return err
		}
	}
	w.upstreams = append(w.upstreams, u)
	return nil
}

// upstreamID resolves a model named in a scenario against the upstream projects,
// so a model can read from another repository's model by its bare name.
func (w *world) upstreamID(name string) string {
	for _, u := range w.upstreams {
		for rel := range u.files {
			if rel == "models/_"+name+".yml" {
				return "model." + u.name + "." + name
			}
		}
	}
	return ""
}

// upstreamUntouched re-reads the upstream project and compares it with what was
// written when it was declared: an upstream donates documentation and is never
// edited, which is a promise about bytes rather than about descriptions.
func (w *world) upstreamUntouched(name string) error {
	for _, u := range w.upstreams {
		if u.name != name {
			continue
		}
		for rel, want := range u.files {
			got, err := os.ReadFile(filepath.Join(u.dir, filepath.FromSlash(rel)))
			if err != nil {
				return fmt.Errorf("%s/%s: %w", name, rel, err)
			}
			if string(got) != want {
				return fmt.Errorf("%s/%s was rewritten:\n--- before\n%s\n--- after\n%s", name, rel, want, got)
			}
		}
		entries, err := os.ReadDir(filepath.Join(u.dir, "models"))
		if err != nil {
			return err
		}
		for _, e := range entries {
			if _, ok := u.files["models/"+e.Name()]; !ok {
				return fmt.Errorf("%s/models/%s was created", name, e.Name())
			}
		}
		return nil
	}
	return fmt.Errorf("no upstream project %q has been declared", name)
}
