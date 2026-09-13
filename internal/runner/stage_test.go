package runner_test

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/rognerud/dbt-ditto/internal/config"
	"github.com/rognerud/dbt-ditto/internal/runner"
)

// A stage is the smallest dbt project that can show every setting doing its
// job: a documented seed, a model inheriting from it, a second seed that
// disagrees, an undocumented source with a documented model below it, and a
// catalog that knows about columns the YAML does not.
type stage struct {
	name       string
	dbtVersion string
	// projectYAML is dbt_project.yml, with the path template left out by default.
	projectYAML string
	nodes       map[string]any
	sources     map[string]any
	catalog     map[string]any
	files       map[string]string
	// extra files written verbatim, e.g. a dbt_loom.config.yml.
	raw map[string]string
}

// column builds a manifest column entry.
func column(name, description string, tweak ...func(map[string]any)) map[string]any {
	c := map[string]any{"name": name, "description": description}
	for _, f := range tweak {
		f(c)
	}
	return c
}

func withColMeta(kv map[string]any) func(map[string]any) {
	return func(c map[string]any) { c["meta"] = kv }
}

func withColTags(tags ...string) func(map[string]any) {
	return func(c map[string]any) { c["tags"] = tags }
}

func modelNode(name, patch string, deps []string, cols ...map[string]any) map[string]any {
	n := map[string]any{
		"unique_id":          "model.demo." + name,
		"name":               name,
		"resource_type":      "model",
		"package_name":       "demo",
		"schema":             "main",
		"database":           "demo",
		"original_file_path": "models/" + name + ".sql",
		"path":               name + ".sql",
		"fqn":                []string{"demo", name},
		"columns":            columnMap(cols),
		"depends_on":         map[string]any{"nodes": deps},
		"access":             "public",
	}
	if patch != "" {
		n["patch_path"] = "demo://" + patch
	}
	return n
}

func seedNode(name string, cols ...map[string]any) map[string]any {
	return map[string]any{
		"unique_id":          "seed.demo." + name,
		"name":               name,
		"resource_type":      "seed",
		"package_name":       "demo",
		"schema":             "main",
		"database":           "demo",
		"original_file_path": "seeds/" + name + ".csv",
		"path":               name + ".csv",
		"fqn":                []string{"demo", name},
		"columns":            columnMap(cols),
		"depends_on":         map[string]any{"nodes": []string{}},
	}
}

func sourceNode(sourceName, name, file string, cols ...map[string]any) map[string]any {
	return map[string]any{
		"unique_id":          "source.demo." + sourceName + "." + name,
		"name":               name,
		"source_name":        sourceName,
		"resource_type":      "source",
		"package_name":       "demo",
		"schema":             "main",
		"database":           "demo",
		"original_file_path": file,
		"path":               file,
		"fqn":                []string{"demo", sourceName, name},
		"columns":            columnMap(cols),
		"depends_on":         map[string]any{"nodes": []string{}},
	}
}

func columnMap(cols []map[string]any) map[string]any {
	out := map[string]any{}
	for _, c := range cols {
		out[c["name"].(string)] = c
	}
	return out
}

// catalogNode builds a catalog entry from name/type pairs in warehouse order;
// a third element sets the comment.
func catalogNode(cols ...[3]string) map[string]any {
	out := map[string]any{}
	for i, c := range cols {
		entry := map[string]any{"name": c[0], "type": c[1], "index": i}
		if c[2] != "" {
			entry["comment"] = c[2]
		}
		out[c[0]] = entry
	}
	return map[string]any{
		"metadata": map[string]any{"name": "", "schema": "main", "database": "demo"},
		"columns":  out,
	}
}

// newStage is the shared fixture: every column here exists to make one setting
// visible, so read settings_test.go alongside it.
func newStage() *stage {
	s := &stage{
		name:       "demo",
		dbtVersion: "1.8.0", // old enough that meta is written top-level
		projectYAML: "name: demo\nversion: \"1.0.0\"\nconfig-version: 2\nprofile: demo\n" +
			"model-paths: [\"models\"]\nseed-paths: [\"seeds\"]\n",
		nodes:   map[string]any{},
		sources: map[string]any{},
		catalog: map[string]any{},
		files:   map[string]string{},
		raw:     map[string]string{},
	}

	// The documented root.
	s.nodes["seed.demo.raw"] = seedNode("raw",
		column("id", "Identifier of the row.",
			withColMeta(map[string]any{"owner": "platform", "pii": true}),
			withColTags("core")),
		column("amount_cents", "Amount, in cents."),
		column("first_name", "Given name, as the customer typed it."),
	)
	s.nodes["seed.demo.raw_alt"] = seedNode("raw_alt",
		column("id", "A different wording for the same column."),
	)

	// The model under test: one column to inherit into, one documented locally, and
	// a catalog that knows about columns the YAML has never listed.
	s.nodes["model.demo.stg"] = modelNode("stg", "models/_stg.yml",
		[]string{"seed.demo.raw", "seed.demo.raw_alt"},
		column("ID", ""),
		column("legacy", "Written by hand, and gone from the warehouse."),
	)
	s.catalog["model.demo.stg"] = catalogNode(
		[3]string{"ID", "INTEGER", ""},
		[3]string{"amount_cents", "BIGINT", ""},
		[3]string{"note", "VARCHAR", "Free text, straight from the warehouse."},
		[3]string{"profile", "STRUCT(first_name VARCHAR)", ""},
	)
	s.files["models/_stg.yml"] = "version: 2\nmodels:\n  - name: stg\n    columns:\n" +
		"      - name: ID\n" +
		"      # a comment that belongs to legacy\n" +
		"      - name: legacy\n        description: Written by hand, and gone from the warehouse.\n"

	// An undocumented source with a documented model below it: the only thing
	// backfill can reach, and the only place a warehouse comment is the sole doc.
	s.sources["source.demo.crm.orders"] = sourceNode("crm", "orders", "models/_sources.yml",
		column("order_id", ""),
	)
	s.files["models/_sources.yml"] = "version: 2\nsources:\n  - name: crm\n    tables:\n" +
		"      - name: orders\n        columns:\n          - name: order_id\n"

	s.nodes["model.demo.stg_orders"] = modelNode("stg_orders", "models/_stg_orders.yml",
		[]string{"source.demo.crm.orders"},
		column("order_id", "Identifier of an order."),
	)
	s.files["models/_stg_orders.yml"] = "version: 2\nmodels:\n  - name: stg_orders\n    columns:\n" +
		"      - name: order_id\n        description: Identifier of an order.\n"

	// A model documented only by a model below it, so backfill's sources_only
	// switch has something to be about.
	s.nodes["model.demo.mid"] = modelNode("mid", "models/_mid.yml",
		[]string{"seed.demo.raw"},
		column("mid_only", ""),
	)
	s.files["models/_mid.yml"] = "version: 2\nmodels:\n  - name: mid\n    columns:\n" +
		"      - name: mid_only\n"
	s.nodes["model.demo.leaf"] = modelNode("leaf", "models/_leaf.yml",
		[]string{"model.demo.mid"},
		column("mid_only", "Documented downstream, where the analyst was looking."),
	)
	s.files["models/_leaf.yml"] = "version: 2\nmodels:\n  - name: leaf\n    columns:\n" +
		"      - name: mid_only\n        description: Documented downstream, where the analyst was looking.\n"

	return s
}

// node returns a node for mutation, failing the test if it is not there.
func (s *stage) node(t *testing.T, id string) map[string]any {
	t.Helper()
	n, ok := s.nodes[id].(map[string]any)
	if !ok {
		n, ok = s.sources[id].(map[string]any)
	}
	if !ok {
		t.Fatalf("stage has no node %q", id)
	}
	return n
}

// col returns a column of a node for mutation.
func (s *stage) col(t *testing.T, nodeID, name string) map[string]any {
	t.Helper()
	cols := s.node(t, nodeID)["columns"].(map[string]any)
	c, ok := cols[name].(map[string]any)
	if !ok {
		t.Fatalf("node %q has no column %q", nodeID, name)
	}
	return c
}

// write puts the stage on disk under dir and returns dir.
func (s *stage) write(t *testing.T, dir string) string {
	t.Helper()
	for _, sub := range []string{"models", "seeds", "target"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(t, filepath.Join(dir, "dbt_project.yml"), s.projectYAML)
	for rel, body := range s.files {
		path := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		writeFile(t, path, body)
	}
	for rel, body := range s.raw {
		path := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		writeFile(t, path, body)
	}
	s.writeArtifacts(t, filepath.Join(dir, "target"))
	return dir
}

// writeArtifacts writes manifest.json and catalog.json into targetDir.
func (s *stage) writeArtifacts(t *testing.T, targetDir string) {
	t.Helper()
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeJSON(t, filepath.Join(targetDir, "manifest.json"), map[string]any{
		"metadata": map[string]any{
			"project_name": s.name,
			"dbt_version":  s.dbtVersion,
			"adapter_type": "duckdb",
		},
		"nodes":   s.nodes,
		"sources": s.sources,
	})
	writeJSON(t, filepath.Join(targetDir, "catalog.json"), map[string]any{
		"metadata": map[string]any{"dbt_version": s.dbtVersion},
		"nodes":    s.catalog,
		"sources":  map[string]any{},
	})
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// snapshot is everything a run is observable by: the YAML, the warnings and the
// notes made while loading the config.
type snapshot struct {
	files    map[string]string
	warnings []string
	notes    []string
	scanned  int
}

// equal reports whether two runs are indistinguishable.
func (s snapshot) equal(other snapshot) bool {
	return s.render() == other.render()
}

func (s snapshot) render() string {
	var b strings.Builder
	for _, name := range sortedKeys(s.files) {
		b.WriteString("--- " + name + "\n" + s.files[name])
	}
	for _, w := range s.warnings {
		b.WriteString("warning: " + w + "\n")
	}
	for _, n := range s.notes {
		b.WriteString("note: " + n + "\n")
	}
	return b.String()
}

// file returns one written YAML file, failing if it is not there.
func (s snapshot) file(t *testing.T, rel string) string {
	t.Helper()
	body, ok := s.files[rel]
	if !ok {
		t.Fatalf("no file %q was written; got %v", rel, sortedKeys(s.files))
	}
	return body
}

// missing asserts a file is not present.
func (s snapshot) missing(t *testing.T, rel string) {
	t.Helper()
	if _, ok := s.files[rel]; ok {
		t.Errorf("file %q still exists", rel)
	}
}

// runStage runs over a project and snapshots the result.
func runStage(t *testing.T, dir string, cfg *config.Config) snapshot {
	t.Helper()
	return runStageWith(t, dir, cfg, runner.Options{Organize: true})
}

// runStageWith is runStage with the run options spelled out.
func runStageWith(t *testing.T, dir string, cfg *config.Config, opts runner.Options) snapshot {
	t.Helper()
	rep, err := runner.Run(cfg, opts)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	// Notes name paths, and a temp directory differs between runs, which would make
	// every comparison report that difference.
	notes := make([]string, 0, len(cfg.Notes)+len(rep.Notes))
	for _, n := range append(append([]string{}, cfg.Notes...), rep.Notes...) {
		notes = append(notes, strings.ReplaceAll(n, dir, "<dir>"))
	}
	snap := snapshot{files: map[string]string{}, notes: notes, scanned: rep.NodesScanned}
	for _, w := range rep.Warnings {
		snap.warnings = append(snap.warnings, w.Node+"."+w.Column+" "+w.Detail)
	}
	sort.Strings(snap.warnings)
	if err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		if !isSchemaYAML(path) {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		snap.files[filepath.ToSlash(rel)] = string(body)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return snap
}

// stageConfig points a config at one stage directory.
func stageConfig(dir string) *config.Config {
	return &config.Config{Dir: dir, Projects: []config.ProjectRef{{Path: dir}}}
}

func ptrTo[T any](v T) *T { return &v }
