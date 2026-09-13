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

// catalogColumn is one column as the warehouse reports it. A scenario needs
// these whenever it is about reconciliation rather than inheritance: adding,
// removing, typing or re-casing a column all start from catalog.json.
type catalogColumn struct {
	name, dataType, comment string
}

// world is the scenario's state: a project, and the runs made over it.
type world struct {
	dir        string
	nodes      []node
	files      map[string]string
	configYAML string
	// catalog holds the warehouse columns per node id; an absent node has no
	// catalog entry, which is a legitimate state dbt itself produces.
	catalog map[string][]catalogColumn
	// artifacts is where manifest.json and catalog.json are written, `target/`
	// unless the scenario says otherwise.
	artifacts string
	// upstreams are the other projects the scenario declared, beside this one.
	upstreams []*upstream
	// tmp holds every directory the scenario made, for removal when it ends.
	tmp []string

	// snapshots of the tree after each run, keyed by run order.
	runs []map[string]string
	rep  *runner.Report
	// cli is the last command run through the binary, for the scenarios that are
	// about what a user types and reads.
	cli *invocation
}

func initScenario(sc *godog.ScenarioContext) {
	w := &world{}

	sc.Given(`^a dbt project$`, w.aProject)
	sc.Given(`^a seed "([^"]*)" with columns:$`, w.aSeed)
	sc.Given(`^a source "([^"]*)\.([^"]*)" with columns:$`, w.aSource)
	sc.Given(`^a model "([^"]*)" reading from "([^"]*)" with columns:$`, w.aModelReadingFromSeed)
	sc.Given(`^a model "([^"]*)" reading from source "([^"]*)\.([^"]*)" with columns:$`, w.aModelReadingFromSource)
	sc.Given(`^a model "([^"]*)" reading from "([^"]*)" and "([^"]*)" with columns:$`, w.aModelWithTwoParents)
	sc.Given(`^a model "([^"]*)" with columns:$`, w.aRootModel)
	sc.Given(`^a seed "([^"]*)" documenting "([^"]*)" as "([^"]*)"$`, w.aDocumentedSeed)
	sc.Given(`^an undocumented model "([^"]*)" reading from "([^"]*)" with column "([^"]*)"$`, w.anUndocumentedModel)
	sc.Given(`^the file "([^"]*)":$`, w.theFile)
	sc.Given(`^the config:$`, w.theConfig)
	sc.Given(`^the warehouse reports for "([^"]*)":$`, w.theCatalog)
	sc.Given(`^the artifacts are in "([^"]*)"$`, w.theArtifactsAreIn)
	sc.Given(`^the node description of "([^"]*)" is "([^"]*)"$`, w.theNodeDescription)
	sc.Given(`^an upstream project "([^"]*)" documenting "([^"]*)\.([^"]*)" as "([^"]*)"$`, w.anUpstreamProject)

	sc.When(`^dbt-ditto runs$`, w.run)
	sc.When(`^dbt-ditto runs in check mode$`, w.runCheck)
	sc.When(`^dbt-ditto runs again with the manifest nodes in reverse order$`, w.runReversed)
	sc.When("^I run `([^`]*)`$", w.runCLI)

	sc.Then(`^the file "([^"]*)" contains:$`, w.fileContains)
	sc.Then(`^the file "([^"]*)" contains, in order:$`, w.fileContainsInOrder)
	sc.Then(`^the file "([^"]*)" does not contain "([^"]*)"$`, w.fileDoesNotContain)
	sc.Then(`^the file "([^"]*)" lists entries in the order:$`, w.fileLists)
	sc.Then(`^the file "([^"]*)" is gone$`, w.fileIsGone)
	sc.Then(`^the two runs are identical once canonicalised$`, w.runsAgreeCanonically)
	sc.Then(`^changes are reported$`, w.changesReported)
	sc.Then(`^no changes are reported$`, w.noChangesReported)
	sc.Then(`^no file on disk changed$`, w.nothingChangedOnDisk)

	sc.Then(`^the exit code is (\d+)$`, w.exitCodeIs)
	sc.Then(`^it prints:$`, w.stdoutContains)
	sc.Then(`^it prints "([^"]*)"$`, w.stdoutContainsLine)
	sc.Then(`^it does not print "([^"]*)"$`, w.stdoutLacks)
	sc.Then(`^it warns:$`, w.stderrContains)
	sc.Then(`^it warns "([^"]*)"$`, w.stderrContainsLine)
	sc.Then(`^it warns about nothing$`, w.stderrSilent)
	sc.Then(`^the upstream project "([^"]*)" was not written to$`, w.upstreamUntouched)

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
	w.dir = w.projectDir()
	w.nodes = nil
	w.files = map[string]string{}
	w.configYAML = ""
	w.catalog = map[string][]catalogColumn{}
	w.artifacts = ""
	w.upstreams = nil
	w.runs = nil
	w.rep = nil
	w.cli = nil
	return nil
}

// columns reads a `| column | description |` table into manifest columns. A
// `meta` cell is inline YAML, so a scenario can declare the markers that live in
// meta without a table per key.
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
		col := map[string]any{"name": name, "description": cell["description"]}
		if raw := cell["meta"]; raw != "" {
			var meta map[string]any
			if err := yaml.Unmarshal([]byte(raw), &meta); err != nil {
				return nil, fmt.Errorf("parse the meta of column %q: %w", name, err)
			}
			col["meta"] = meta
		}
		out[name] = col
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
	return w.addModel(name, []string{w.parentID(parent)}, tbl)
}

func (w *world) aModelReadingFromSource(name, sourceName, table string, tbl *godog.Table) error {
	return w.addModel(name, []string{"source.demo." + sourceName + "." + table}, tbl)
}

// aRootModel is a model with no parents, for the scenarios about reconciling a
// model against the warehouse rather than against the DAG.
func (w *world) aRootModel(name string, tbl *godog.Table) error {
	return w.addModel(name, []string{}, tbl)
}

func (w *world) aModelWithTwoParents(name, first, second string, tbl *godog.Table) error {
	return w.addModel(name, []string{w.parentID(first), w.parentID(second)}, tbl)
}

// parentID resolves a parent written by its bare name against what the scenario
// has already declared, so a model may read from a seed or from another model
// without the scenario spelling out unique_ids.
func (w *world) parentID(name string) string {
	for _, n := range w.nodes {
		if got, _ := n.body["name"].(string); got == name {
			return n.id
		}
	}
	if id := w.upstreamID(name); id != "" {
		return id
	}
	return "seed.demo." + name
}

// aDocumentedSeed and anUndocumentedModel are the smallest DAG documentation can
// flow down, as one line each: the scenarios about flags and about the config
// file are not about the shape of the project, and spelling one out in tables
// would bury what they are about.
func (w *world) aDocumentedSeed(name, column, description string) error {
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
		"columns": map[string]any{
			column: map[string]any{"name": column, "description": description},
		},
		"depends_on": map[string]any{"nodes": []string{}},
		"config":     map[string]any{"dbt-osmosis": "_seeds.yml"},
	}})
	return nil
}

func (w *world) anUndocumentedModel(name, parent, column string) error {
	w.addModelColumns(name, []string{w.parentID(parent)}, map[string]any{
		column: map[string]any{"name": column, "description": ""},
	})
	return w.theFile("models/_"+name+".yml", &godog.DocString{Content: fmt.Sprintf(
		"version: 2\nmodels:\n  - name: %s\n    columns:\n      - name: %s\n", name, column)})
}

// theNodeDescription documents the table itself rather than one of its columns,
// which is a separate thing to inherit.
func (w *world) theNodeDescription(nodeName, description string) error {
	for i := range w.nodes {
		if w.nodes[i].id == w.nodeID(nodeName) {
			w.nodes[i].body["description"] = description
			return nil
		}
	}
	return fmt.Errorf("no node named %q has been declared", nodeName)
}

// theArtifactsAreIn puts manifest.json and catalog.json somewhere other than
// `target/`, which is what a CI job that downloads them does.
func (w *world) theArtifactsAreIn(dir string) error {
	w.artifacts = dir
	return nil
}

// target is the directory the artifacts are written to, `target/` unless the
// scenario moved them.
func (w *world) target() string {
	if w.artifacts != "" {
		return w.artifacts
	}
	return "target"
}

// theCatalog records what `dbt docs generate` found for a node: the column list
// the warehouse reports, with its types and its COMMENTs.
func (w *world) theCatalog(nodeName string, tbl *godog.Table) error {
	if len(tbl.Rows) == 0 {
		return fmt.Errorf("the warehouse table is empty")
	}
	head := make([]string, 0, len(tbl.Rows[0].Cells))
	for _, c := range tbl.Rows[0].Cells {
		head = append(head, strings.TrimSpace(c.Value))
	}
	var cols []catalogColumn
	for _, row := range tbl.Rows[1:] {
		cell := map[string]string{}
		for i, c := range row.Cells {
			if i < len(head) {
				cell[head[i]] = strings.TrimSpace(c.Value)
			}
		}
		if cell["column"] == "" {
			return fmt.Errorf("a row has no column name")
		}
		cols = append(cols, catalogColumn{
			name:     cell["column"],
			dataType: cell["data_type"],
			comment:  cell["comment"],
		})
	}
	id := w.nodeID(nodeName)
	if id == "" {
		return fmt.Errorf("no node named %q has been declared", nodeName)
	}
	w.catalog[id] = cols
	return nil
}

// nodeID finds a declared node by its bare name, or by `source.table` for a
// source, which is how the scenarios name them.
func (w *world) nodeID(name string) string {
	for _, n := range w.nodes {
		if got, _ := n.body["name"].(string); got == name {
			return n.id
		}
		if src, _ := n.body["source_name"].(string); src != "" {
			if got, _ := n.body["name"].(string); src+"."+got == name {
				return n.id
			}
		}
	}
	return ""
}

func (w *world) addModel(name string, deps []string, tbl *godog.Table) error {
	cols, err := columns(tbl)
	if err != nil {
		return err
	}
	w.addModelColumns(name, deps, cols)
	return nil
}

func (w *world) addModelColumns(name string, deps []string, cols map[string]any) {
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
		w.dir = w.projectDir()
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
	for _, sub := range []string{"models", "seeds", w.target()} {
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

	target := filepath.Join(w.dir, w.target())
	if err := os.MkdirAll(target, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(target, "manifest.json"), []byte(b.String()), 0o644); err != nil {
		return err
	}
	return w.writeCatalog(target)
}

// writeCatalog emits catalog.json from what the scenario said the warehouse
// reports. A scenario that says nothing gets an empty catalog, which is a state
// dbt itself produces and which the manifest-only scenarios rely on.
func (w *world) writeCatalog(target string) error {
	type catColumn struct {
		Name    string `json:"name"`
		Type    string `json:"type"`
		Comment string `json:"comment"`
		Index   int    `json:"index"`
	}
	type catEntry struct {
		Metadata map[string]string    `json:"metadata"`
		Columns  map[string]catColumn `json:"columns"`
	}
	doc := map[string]any{
		"metadata": map[string]string{"dbt_version": "1.8.0"},
		"nodes":    map[string]catEntry{},
		"sources":  map[string]catEntry{},
	}
	for _, n := range w.nodes {
		cols, ok := w.catalog[n.id]
		if !ok {
			continue
		}
		entry := catEntry{
			Metadata: map[string]string{
				"name":     n.body["name"].(string),
				"schema":   n.body["schema"].(string),
				"database": n.body["database"].(string),
			},
			Columns: map[string]catColumn{},
		}
		for i, c := range cols {
			entry.Columns[c.name] = catColumn{Name: c.name, Type: c.dataType, Comment: c.comment, Index: i}
		}
		key := "nodes"
		if n.source {
			key = "sources"
		}
		doc[key].(map[string]catEntry)[n.id] = entry
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(target, "catalog.json"), raw, 0o644)
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

// fileContainsInOrder is fileContains plus the order of the lines, which is the
// only way to state a claim about column order.
func (w *world) fileContainsInOrder(rel string, want *godog.DocString) error {
	body, err := w.file(rel)
	if err != nil {
		return err
	}
	at := 0
	for _, line := range strings.Split(strings.TrimSpace(want.Content), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		i := strings.Index(body[at:], line)
		if i < 0 {
			return fmt.Errorf("%s does not contain %q after the line before it:\n%s", rel, line, body)
		}
		at += i + len(line)
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

// fileIsGone asserts a schema file no longer exists, which is what organising a
// model out of its last file leaves behind.
func (w *world) fileIsGone(rel string) error {
	snap, err := w.latest()
	if err != nil {
		return err
	}
	if body, ok := snap[rel]; ok {
		return fmt.Errorf("%s still exists:\n%s", rel, body)
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
		// The snapshot holds schema YAML only, so a config file the scenario wrote
		// is not something this comparison can speak about.
		if !canon.IsSchemaFile(filepath.Base(rel)) {
			continue
		}
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

// projectDir puts the project in a directory named `project`, because the run
// reports a file by its path from the project directory's own name: with a
// temporary name there, no documented output could be quoted.
func (w *world) projectDir() string {
	return filepath.Join(w.tempDir(), "project")
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
