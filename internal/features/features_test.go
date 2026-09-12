// Package features runs the Gherkin specifications in features/ against the
// real pipeline: the promises stated in an analyst's language, driving
// runner.Run over a project in a temporary directory, with nothing mocked.
package features

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/cucumber/godog"
	"github.com/rognerud/dbt-ditto/internal/canon"
	"github.com/rognerud/dbt-ditto/internal/config"
	"github.com/rognerud/dbt-ditto/internal/runner"
	"github.com/rognerud/dbt-ditto/internal/yamlfile"
	"gopkg.in/yaml.v3"
)

func TestFeatures(t *testing.T) {
	suite := godog.TestSuite{
		ScenarioInitializer: initScenario,
		Options: &godog.Options{
			Format:   "pretty",
			Paths:    []string{"../../features"},
			TestingT: t,
			Strict:   true,
		},
	}
	if suite.Run() != 0 {
		t.Fatal("the feature specifications in features/ are not satisfied")
	}
}

// node is one manifest entry, kept with its id so the manifest can be written
// in a chosen order — what the manifest-order feature is about.
type node struct {
	id     string
	source bool
	body   map[string]any
}

// world is the scenario's state: a project, and the runs made over it.
type world struct {
	dir        string
	nodes      []node
	files      map[string]string
	configYAML string
	// tmp holds every directory the scenario made, for removal when it ends.
	tmp []string

	// snapshots of the tree after each run, keyed by run order.
	runs []map[string]string
	rep  *runner.Report
}

func initScenario(sc *godog.ScenarioContext) {
	w := &world{}

	sc.Given(`^a dbt project$`, w.aProject)
	sc.Given(`^a seed "([^"]*)" with columns:$`, w.aSeed)
	sc.Given(`^a source "([^"]*)\.([^"]*)" with columns:$`, w.aSource)
	sc.Given(`^a model "([^"]*)" reading from "([^"]*)" with columns:$`, w.aModelReadingFromSeed)
	sc.Given(`^a model "([^"]*)" reading from source "([^"]*)\.([^"]*)" with columns:$`, w.aModelReadingFromSource)
	sc.Given(`^the file "([^"]*)":$`, w.theFile)
	sc.Given(`^the config:$`, w.theConfig)

	sc.When(`^dbt-ditto runs$`, w.run)
	sc.When(`^dbt-ditto runs in check mode$`, w.runCheck)
	sc.When(`^dbt-ditto runs again with the manifest nodes in reverse order$`, w.runReversed)

	sc.Then(`^the file "([^"]*)" contains:$`, w.fileContains)
	sc.Then(`^the file "([^"]*)" does not contain "([^"]*)"$`, w.fileDoesNotContain)
	sc.Then(`^the file "([^"]*)" lists entries in the order:$`, w.fileLists)
	sc.Then(`^the two runs are identical once canonicalised$`, w.runsAgreeCanonically)
	sc.Then(`^changes are reported$`, w.changesReported)
	sc.Then(`^no changes are reported$`, w.noChangesReported)
	sc.Then(`^no file on disk changed$`, w.nothingChangedOnDisk)

	sc.After(func(ctx context.Context, _ *godog.Scenario, err error) (context.Context, error) {
		for _, dir := range w.tmp {
			os.RemoveAll(dir)
		}
		w.tmp = nil
		return ctx, err
	})
}

// --- Given ------------------------------------------------------------------

func (w *world) aProject() error {
	w.dir = w.tempDir()
	w.nodes = nil
	w.files = map[string]string{}
	w.configYAML = ""
	w.runs = nil
	w.rep = nil
	return nil
}

// columns reads a `| column | description |` table into manifest columns.
func columns(tbl *godog.Table) (map[string]any, error) {
	if len(tbl.Rows) == 0 {
		return nil, fmt.Errorf("the column table is empty")
	}
	head := make([]string, 0, len(tbl.Rows[0].Cells))
	for _, c := range tbl.Rows[0].Cells {
		head = append(head, strings.TrimSpace(c.Value))
	}
	out := map[string]any{}
	for _, row := range tbl.Rows[1:] {
		cell := map[string]string{}
		for i, c := range row.Cells {
			if i < len(head) {
				cell[head[i]] = strings.TrimSpace(c.Value)
			}
		}
		name := cell["column"]
		if name == "" {
			return nil, fmt.Errorf("a row has no column name")
		}
		out[name] = map[string]any{"name": name, "description": cell["description"]}
	}
	return out, nil
}

func (w *world) aSeed(name string, tbl *godog.Table) error {
	cols, err := columns(tbl)
	if err != nil {
		return err
	}
	w.nodes = append(w.nodes, node{id: "seed.demo." + name, body: map[string]any{
		"unique_id":          "seed.demo." + name,
		"name":               name,
		"resource_type":      "seed",
		"package_name":       "demo",
		"schema":             "main",
		"database":           "demo",
		"original_file_path": "seeds/" + name + ".csv",
		"path":               name + ".csv",
		"fqn":                []string{"demo", name},
		"columns":            cols,
		"depends_on":         map[string]any{"nodes": []string{}},
		// Every seed shares one schema file, which is what makes order observable.
		"config": map[string]any{"dbt-osmosis": "_seeds.yml"},
	}})
	return nil
}

func (w *world) aSource(sourceName, name string, tbl *godog.Table) error {
	cols, err := columns(tbl)
	if err != nil {
		return err
	}
	id := "source.demo." + sourceName + "." + name
	w.nodes = append(w.nodes, node{id: id, source: true, body: map[string]any{
		"unique_id":          id,
		"name":               name,
		"source_name":        sourceName,
		"resource_type":      "source",
		"package_name":       "demo",
		"schema":             "main",
		"database":           "demo",
		"original_file_path": "models/_sources.yml",
		"path":               "models/_sources.yml",
		"fqn":                []string{"demo", sourceName, name},
		"columns":            cols,
		"depends_on":         map[string]any{"nodes": []string{}},
	}})
	return nil
}

func (w *world) aModelReadingFromSeed(name, parent string, tbl *godog.Table) error {
	return w.addModel(name, []string{"seed.demo." + parent}, tbl)
}

func (w *world) aModelReadingFromSource(name, sourceName, table string, tbl *godog.Table) error {
	return w.addModel(name, []string{"source.demo." + sourceName + "." + table}, tbl)
}

func (w *world) addModel(name string, deps []string, tbl *godog.Table) error {
	cols, err := columns(tbl)
	if err != nil {
		return err
	}
	w.nodes = append(w.nodes, node{id: "model.demo." + name, body: map[string]any{
		"unique_id":          "model.demo." + name,
		"name":               name,
		"resource_type":      "model",
		"package_name":       "demo",
		"schema":             "main",
		"database":           "demo",
		"original_file_path": "models/" + name + ".sql",
		"path":               name + ".sql",
		"fqn":                []string{"demo", name},
		"columns":            cols,
		"depends_on":         map[string]any{"nodes": deps},
		"access":             "public",
		// One file per model, as the fixture project lays them out.
		"config": map[string]any{"dbt-osmosis": "_{model}.yml"},
	}})
	return nil
}

func (w *world) theFile(rel string, body *godog.DocString) error {
	w.files[rel] = strings.TrimLeft(body.Content, "\n") + "\n"
	// A node documented in a file has to say so, as dbt records a patch_path, or
	// the run would treat it as undocumented and write elsewhere.
	for i := range w.nodes {
		if w.nodes[i].source {
			continue
		}
		name, _ := w.nodes[i].body["name"].(string)
		if strings.Contains(w.files[rel], "- name: "+name+"\n") {
			w.nodes[i].body["patch_path"] = "demo://" + rel
		}
	}
	return nil
}

func (w *world) theConfig(body *godog.DocString) error {
	w.configYAML = body.Content
	return nil
}

// --- When -------------------------------------------------------------------

func (w *world) run() error { return w.execute(runner.Options{Organize: true}, false) }

func (w *world) runCheck() error {
	return w.execute(runner.Options{Organize: true, DryRun: true, Check: true}, false)
}

func (w *world) runReversed() error {
	return w.execute(runner.Options{Organize: true}, true)
}

// execute writes the project out and runs the pipeline over it.
func (w *world) execute(opts runner.Options, reverse bool) error {
	if len(w.runs) == 0 {
		if err := w.writeProject(reverse); err != nil {
			return err
		}
	} else if reverse {
		// A reversed run describes the same project with a differently ordered
		// manifest, so it needs its own copy of the tree.
		w.dir = w.tempDir()
		if err := w.writeProject(true); err != nil {
			return err
		}
	} else if err := w.writeManifest(false); err != nil {
		return err
	}

	cfg, err := w.config()
	if err != nil {
		return err
	}
	rep, err := runner.Run(cfg, opts)
	if err != nil {
		return fmt.Errorf("run: %w", err)
	}
	w.rep = rep
	snap, err := w.snapshot()
	if err != nil {
		return err
	}
	w.runs = append(w.runs, snap)
	return nil
}

// config assembles the dbt_ditto.yml the scenario asked for and loads it.
func (w *world) config() (*config.Config, error) {
	if w.configYAML == "" {
		return config.Default(w.dir), nil
	}
	var c config.Config
	if err := yaml.Unmarshal([]byte(w.configYAML), &c); err != nil {
		return nil, fmt.Errorf("parse the scenario's config: %w", err)
	}
	c.Dir = w.dir
	c.Projects = []config.ProjectRef{{Path: w.dir}}
	return &c, nil
}

func (w *world) writeProject(reverse bool) error {
	for _, sub := range []string{"models", "seeds", "target"} {
		if err := os.MkdirAll(filepath.Join(w.dir, sub), 0o755); err != nil {
			return err
		}
	}
	project := "name: demo\nversion: \"1.0.0\"\nconfig-version: 2\nprofile: demo\n" +
		"model-paths: [\"models\"]\nseed-paths: [\"seeds\"]\n"
	if err := os.WriteFile(filepath.Join(w.dir, "dbt_project.yml"), []byte(project), 0o644); err != nil {
		return err
	}
	for rel, body := range w.files {
		path := filepath.Join(w.dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			return err
		}
	}
	return w.writeManifest(reverse)
}

// writeManifest emits manifest.json in a chosen node order, assembled by hand
// because a Go map has none.
func (w *world) writeManifest(reverse bool) error {
	ordered := make([]node, len(w.nodes))
	copy(ordered, w.nodes)
	if reverse {
		for i, j := 0, len(ordered)-1; i < j; i, j = i+1, j-1 {
			ordered[i], ordered[j] = ordered[j], ordered[i]
		}
	}
	var b strings.Builder
	b.WriteString(`{"metadata":{"project_name":"demo","dbt_version":"1.8.0","adapter_type":"duckdb"}`)
	for _, key := range []string{"nodes", "sources"} {
		b.WriteString(`,"` + key + `":{`)
		first := true
		for _, n := range ordered {
			if n.source != (key == "sources") {
				continue
			}
			raw, err := json.Marshal(n.body)
			if err != nil {
				return err
			}
			if !first {
				b.WriteString(",")
			}
			first = false
			b.WriteString(jsonKey(n.id) + ":" + string(raw))
		}
		b.WriteString("}")
	}
	b.WriteString("}")

	target := filepath.Join(w.dir, "target")
	if err := os.MkdirAll(target, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(target, "manifest.json"), []byte(b.String()), 0o644); err != nil {
		return err
	}
	// An empty catalog: these scenarios are about the manifest, and a missing
	// catalog is a legitimate state.
	catalog := `{"metadata":{"dbt_version":"1.8.0"},"nodes":{},"sources":{}}`
	return os.WriteFile(filepath.Join(target, "catalog.json"), []byte(catalog), 0o644)
}

// jsonKey quotes a JSON object key.
func jsonKey(s string) string {
	raw, _ := json.Marshal(s)
	return string(raw)
}

// snapshot reads every schema file in the project.
func (w *world) snapshot() (map[string]string, error) {
	out := map[string]string{}
	err := filepath.Walk(w.dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		rel, err := filepath.Rel(w.dir, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if strings.HasPrefix(rel, "target/") || !canon.IsSchemaFile(filepath.Base(path)) {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		out[rel] = string(body)
		return nil
	})
	return out, err
}

// --- Then -------------------------------------------------------------------

func (w *world) latest() (map[string]string, error) {
	if len(w.runs) == 0 {
		return nil, fmt.Errorf("nothing has been run yet")
	}
	return w.runs[len(w.runs)-1], nil
}

func (w *world) file(rel string) (string, error) {
	snap, err := w.latest()
	if err != nil {
		return "", err
	}
	body, ok := snap[rel]
	if !ok {
		names := make([]string, 0, len(snap))
		for name := range snap {
			names = append(names, name)
		}
		sort.Strings(names)
		return "", fmt.Errorf("no file %q was written; got %v", rel, names)
	}
	return body, nil
}

func (w *world) fileContains(rel string, want *godog.DocString) error {
	body, err := w.file(rel)
	if err != nil {
		return err
	}
	for _, line := range strings.Split(strings.TrimSpace(want.Content), "\n") {
		if !strings.Contains(body, strings.TrimSpace(line)) {
			return fmt.Errorf("%s does not contain %q:\n%s", rel, strings.TrimSpace(line), body)
		}
	}
	return nil
}

func (w *world) fileDoesNotContain(rel, want string) error {
	body, err := w.file(rel)
	if err != nil {
		return err
	}
	if strings.Contains(body, want) {
		return fmt.Errorf("%s contains %q, and should not:\n%s", rel, want, body)
	}
	return nil
}

// fileLists asserts the order of the named entries in a file, the one thing
// manifest order decides.
func (w *world) fileLists(rel string, tbl *godog.Table) error {
	body, err := w.file(rel)
	if err != nil {
		return err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(body), &doc); err != nil {
		return err
	}
	var got []string
	for _, key := range canon.EntryKeys {
		seq := yamlfile.MapGet(doc.Content[0], key)
		if seq == nil || seq.Kind != yaml.SequenceNode {
			continue
		}
		for _, e := range seq.Content {
			got = append(got, yamlfile.StringOf(yamlfile.MapGet(e, "name")))
		}
	}
	var want []string
	for _, row := range tbl.Rows[1:] {
		want = append(want, strings.TrimSpace(row.Cells[0].Value))
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		return fmt.Errorf("%s lists %v, expected %v:\n%s", rel, got, want, body)
	}
	return nil
}

// runsAgreeCanonically is the parity proof's own assumption as a behaviour:
// whatever order dbt listed the nodes in, canonicalising leaves the same bytes.
func (w *world) runsAgreeCanonically() error {
	if len(w.runs) < 2 {
		return fmt.Errorf("two runs are needed, got %d", len(w.runs))
	}
	first, err := canonical(w.runs[len(w.runs)-2])
	if err != nil {
		return err
	}
	second, err := canonical(w.runs[len(w.runs)-1])
	if err != nil {
		return err
	}
	if first != second {
		return fmt.Errorf("the two runs differ:\n--- first\n%s\n--- second\n%s", first, second)
	}
	return nil
}

// canonical renders a snapshot with every file canonicalised, so the comparison
// is about content rather than append order.
func canonical(snap map[string]string) (string, error) {
	dir, err := os.MkdirTemp("", "canon")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	for rel, body := range snap {
		path := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return "", err
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			return "", err
		}
	}
	if err := canon.Dir(dir); err != nil {
		return "", err
	}
	var names []string
	for rel := range snap {
		names = append(names, rel)
	}
	sort.Strings(names)
	var b strings.Builder
	for _, rel := range names {
		body, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
		if err != nil {
			return "", err
		}
		b.WriteString("--- " + rel + "\n" + string(body))
	}
	return b.String(), nil
}

func (w *world) changesReported() error {
	if w.rep == nil {
		return fmt.Errorf("nothing has been run yet")
	}
	if len(w.rep.FilesWritten) == 0 {
		return fmt.Errorf("the run reported no changes")
	}
	return nil
}

func (w *world) noChangesReported() error {
	if w.rep == nil {
		return fmt.Errorf("nothing has been run yet")
	}
	if len(w.rep.FilesWritten) > 0 {
		return fmt.Errorf("the run reported changes to %v", w.rep.FilesWritten)
	}
	return nil
}

// nothingChangedOnDisk compares the tree after the run against the project as
// it was described.
func (w *world) nothingChangedOnDisk() error {
	snap, err := w.latest()
	if err != nil {
		return err
	}
	for rel, want := range w.files {
		got, ok := snap[rel]
		if !ok {
			return fmt.Errorf("%s was deleted", rel)
		}
		if got != want {
			return fmt.Errorf("%s was rewritten:\n--- before\n%s\n--- after\n%s", rel, want, got)
		}
	}
	for rel := range snap {
		if _, ok := w.files[rel]; !ok {
			return fmt.Errorf("%s was created", rel)
		}
	}
	return nil
}

// tempDir makes a directory the scenario owns.
func (w *world) tempDir() string {
	dir, err := os.MkdirTemp("", "ditto-features")
	if err != nil {
		panic(err)
	}
	w.tmp = append(w.tmp, dir)
	return dir
}
