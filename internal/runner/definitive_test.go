package runner_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rognerud/dbt-ditto/internal/config"
	"github.com/rognerud/dbt-ditto/internal/inherit"
	"github.com/rognerud/dbt-ditto/internal/runner"
)

// markDefinitive declares a column settled, in the manifest and in the YAML the
// manifest was parsed from.
func markDefinitive(t *testing.T, s *stage, nodeID, file, column, description string) {
	t.Helper()
	c := s.col(t, nodeID, column)
	c["description"] = description
	c["meta"] = map[string]any{inherit.DefinitiveKey: true}

	body, ok := s.files[file]
	if !ok {
		t.Fatalf("stage has no file %q", file)
	}
	entry := "      - name: " + column + "\n"
	if !strings.Contains(body, entry) {
		t.Fatalf("file %q does not list column %q:\n%s", file, column, body)
	}
	s.files[file] = strings.Replace(body, entry,
		entry+"        description: "+description+"\n"+
			"        meta:\n          "+inherit.DefinitiveKey+": true\n", 1)
}

// A definitive declaration is a decision, so it travels in every direction: to
// the models below it, to the seed above it, and into the other projects in the
// run. Ordinary inheritance only ever goes downhill.
func TestDefinitiveTravelsUpDownAndAcross(t *testing.T) {
	s := newStage()
	// Declared on the model in the middle: the seed above documents `id`
	// differently, and `raw_alt` differently again.
	markDefinitive(t, s, "model.demo.stg", "models/_stg.yml", "ID", "The settled wording for the identifier.")
	// A second project, with its own column of the same name.
	s.nodes["model.demo.other"] = modelNode("other", "models/_other.yml", nil,
		column("id", "Something else entirely."))
	s.files["models/_other.yml"] = "version: 2\nmodels:\n  - name: other\n    columns:\n" +
		"      - name: id\n        description: Something else entirely.\n"
	// And a seed above, which inheritance could never reach from below.
	dir := s.write(t, t.TempDir())

	snap := runStage(t, dir, stageConfig(dir))

	contains(t, snap.file(t, "models/_stg.yml"), "The settled wording for the identifier.",
		"the declaring column did not keep its own wording")
	contains(t, snap.file(t, "models/_other.yml"), "The settled wording for the identifier.",
		"a column in another part of the graph did not take the settled wording")
	lacks(t, snap.file(t, "models/_other.yml"), "Something else entirely.",
		"the settled wording did not replace the local one")
}

// The column carrying the marker is the decision. Nothing overwrites it — not
// an ancestor, not `force`.
func TestDefinitiveColumnIsLocked(t *testing.T) {
	s := newStage()
	markDefinitive(t, s, "model.demo.stg", "models/_stg.yml", "ID", "Locked, and not up for discussion.")
	dir := s.write(t, t.TempDir())

	cfg := stageConfig(dir)
	cfg.Inheritance.Force = ptrTo(true)
	snap := runStage(t, dir, cfg)

	body := snap.file(t, "models/_stg.yml")
	contains(t, body, "Locked, and not up for discussion.", "force overwrote a definitive column")
	lacks(t, body, "Identifier of the row.", "the upstream wording was written over the decision")
}

// Whoever reads the file afterwards needs to see where the wording came from,
// exactly as they would for an ordinary inherited description.
func TestDefinitiveIsRecordedAsTheProgenitor(t *testing.T) {
	s := newStage()
	markDefinitive(t, s, "model.demo.stg", "models/_stg.yml", "ID", "The settled wording.")
	s.nodes["model.demo.other"] = modelNode("other", "models/_other.yml", nil, column("id", ""))
	s.files["models/_other.yml"] = "version: 2\nmodels:\n  - name: other\n    columns:\n      - name: id\n"
	dir := s.write(t, t.TempDir())

	snap := runStage(t, dir, stageConfig(dir))

	contains(t, snap.file(t, "models/_other.yml"), "osmosis_progenitor: model.demo.stg",
		"the settled wording did not record where the decision was made")
}

// The marker names one column as the decision. Copying it downstream would make
// every column below claim to be the decision as well.
func TestDefinitiveMarkerIsNotInherited(t *testing.T) {
	s := newStage()
	// The marker is on the seed's column, and the model below only inherits.
	seedCol := s.col(t, "seed.demo.raw", "id")
	seedCol["description"] = "The settled wording."
	seedCol["meta"] = map[string]any{inherit.DefinitiveKey: true, "owner": "platform"}
	dir := s.write(t, t.TempDir())

	snap := runStage(t, dir, stageConfig(dir))

	body := snap.file(t, "models/_stg.yml")
	contains(t, body, "The settled wording.", "the settled wording did not arrive")
	lacks(t, body, inherit.DefinitiveKey, "the marker was copied to a column that did not declare it")
}

// A disagreement between two declarations is not something to resolve by rule:
// the run stops and says which ones disagree.
func TestConflictingDefinitivesAreAnError(t *testing.T) {
	s := newStage()
	markDefinitive(t, s, "model.demo.stg", "models/_stg.yml", "ID", "One decision.")
	markDefinitive(t, s, "model.demo.leaf", "models/_leaf.yml", "mid_only", "Unrelated, and fine.")
	s.nodes["model.demo.other"] = modelNode("other", "models/_other.yml", nil,
		column("id", "A different decision.", withColMeta(map[string]any{inherit.DefinitiveKey: true})))
	s.files["models/_other.yml"] = "version: 2\nmodels:\n  - name: other\n    columns:\n" +
		"      - name: id\n        description: A different decision.\n        meta:\n" +
		"          " + inherit.DefinitiveKey + ": true\n"
	dir := s.write(t, t.TempDir())

	before := readTree(t, dir)
	_, err := runner.Run(stageConfig(dir), runner.Options{Organize: true})
	if err == nil {
		t.Fatal("two conflicting declarations were accepted")
	}
	msg := err.Error()
	for _, want := range []string{
		"model.demo.stg.ID", "model.demo.other.id", "One decision.", "A different decision.",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("error does not name %q:\n%s", want, msg)
		}
	}
	if !strings.Contains(msg, "case_insensitive") {
		t.Errorf("error does not explain why ID and id are the same column:\n%s", msg)
	}
	if got := readTree(t, dir); !equalTrees(before, got) {
		t.Error("files were written even though the run failed")
	}
}

// The same decision written down in two places is one decision, not a conflict.
func TestIdenticalDefinitivesAgree(t *testing.T) {
	s := newStage()
	markDefinitive(t, s, "model.demo.stg", "models/_stg.yml", "ID", "The one wording.")
	s.nodes["model.demo.other"] = modelNode("other", "models/_other.yml", nil,
		column("id", "The one wording.", withColMeta(map[string]any{inherit.DefinitiveKey: true})))
	s.files["models/_other.yml"] = "version: 2\nmodels:\n  - name: other\n    columns:\n" +
		"      - name: id\n        description: The one wording.\n        meta:\n" +
		"          " + inherit.DefinitiveKey + ": true\n"
	dir := s.write(t, t.TempDir())

	if _, err := runner.Run(stageConfig(dir), runner.Options{Organize: true}); err != nil {
		t.Fatalf("identical declarations were treated as a conflict: %v", err)
	}
}

// With case-sensitive matching, `ID` and `id` are different columns, so two
// declarations are two decisions about two things.
func TestDefinitivesInDifferentCasesDoNotConflictWhenMatchingIsCaseSensitive(t *testing.T) {
	s := newStage()
	markDefinitive(t, s, "model.demo.stg", "models/_stg.yml", "ID", "About the upper-cased one.")
	s.nodes["model.demo.other"] = modelNode("other", "models/_other.yml", nil,
		column("id", "About the lower-cased one.", withColMeta(map[string]any{inherit.DefinitiveKey: true})))
	s.files["models/_other.yml"] = "version: 2\nmodels:\n  - name: other\n    columns:\n" +
		"      - name: id\n        description: About the lower-cased one.\n        meta:\n" +
		"          " + inherit.DefinitiveKey + ": true\n"
	dir := s.write(t, t.TempDir())

	cfg := stageConfig(dir)
	cfg.Inheritance.CaseInsensitive = ptrTo(false)
	if _, err := runner.Run(cfg, runner.Options{Organize: true}); err != nil {
		t.Fatalf("two case-sensitively distinct columns were treated as a conflict: %v", err)
	}
}

// A declaration in a manifest that arrived through dbt-loom cannot be reviewed
// or edited from here, so it does not get to rewrite local documentation.
func TestDefinitiveInAManifestOnlyProjectIsIgnored(t *testing.T) {
	root := t.TempDir()
	down := newStage()
	delete(down.nodes, "seed.demo.raw_alt")
	dir := down.write(t, filepath.Join(root, "downstream"))

	up := newStage()
	up.name = "upstream"
	up.sources = map[string]any{}
	up.nodes = map[string]any{
		"seed.upstream.raw": map[string]any{
			"unique_id": "seed.upstream.raw", "name": "raw",
			"resource_type": "seed", "package_name": "upstream",
			"columns": columnMap([]map[string]any{
				column("ID", "Declared settled somewhere nobody here can see.",
					withColMeta(map[string]any{inherit.DefinitiveKey: true})),
			}),
			"depends_on": map[string]any{"nodes": []string{}},
		},
	}
	upDir := filepath.Join(root, "upstream")
	if err := os.MkdirAll(upDir, 0o755); err != nil {
		t.Fatal(err)
	}
	up.writeArtifacts(t, upDir)

	cfg := &config.Config{Dir: root, Projects: []config.ProjectRef{
		{Path: dir},
		{Manifest: filepath.Join(upDir, "manifest.json"), Upstream: true},
	}}
	snap := runStage(t, dir, cfg)

	body := snap.file(t, "models/_stg.yml")
	lacks(t, body, inherit.DefinitiveKey,
		"a marker from a manifest-only project reached the local YAML")
	contains(t, body, "Identifier of the row.",
		"the local seed's documentation was replaced by a manifest-only declaration")
}

// A settled column has nothing arbitrary left about it, so the disagreement
// that prompted the declaration stops being reported.
func TestDefinitiveSilencesTheAmbiguityWarning(t *testing.T) {
	s := newStage()
	markDefinitive(t, s, "model.demo.stg", "models/_stg.yml", "ID", "Settled.")
	dir := s.write(t, t.TempDir())

	snap := runStage(t, dir, stageConfig(dir))

	if hasWarning(snap, "documented differently") {
		t.Errorf("a settled column was still reported as ambiguous: %v", snap.warnings)
	}
}

func readTree(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	if err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !isSchemaYAML(path) {
			return err
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, path)
		out[filepath.ToSlash(rel)] = string(body)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return out
}

func equalTrees(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}
