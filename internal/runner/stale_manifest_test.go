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

// A schema file is almost always newer than the manifest beside it: dbt wrote
// `manifest.json` at the last parse, and anything typed into the YAML since —
// or written there by a previous dbt-ditto run — is in the file and not in the
// artifact. Everything here is about reading the file rather than assuming the
// manifest already contains it.

// writeStage lays out the shared stage and returns its directory and config.
func writeStage(t *testing.T, fn func(*stage)) (string, *config.Config) {
	t.Helper()
	s := newStage()
	if fn != nil {
		fn(s)
	}
	dir := s.write(t, t.TempDir())
	return dir, stageConfig(dir)
}

func readFile(t *testing.T, dir, rel string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// A meta key that exists only in the YAML used to be deleted: the writer
// rebuilds `meta:` from what was resolved, and what was resolved was seeded
// from the manifest alone.
func TestMetaWrittenOnlyInTheYAMLSurvives(t *testing.T) {
	dir, cfg := writeStage(t, func(s *stage) {
		s.files["models/_stg.yml"] = "version: 2\nmodels:\n  - name: stg\n    columns:\n" +
			"      - name: ID\n        meta:\n          typed_by_hand: 1\n"
	})
	if _, err := runner.Run(cfg, runner.Options{Organize: true}); err != nil {
		t.Fatal(err)
	}
	if body := readFile(t, dir, "models/_stg.yml"); !strings.Contains(body, "typed_by_hand") {
		t.Errorf("a meta key the manifest has never seen was deleted:\n%s", body)
	}
}

// The marker's whole purpose is to settle a column. Read from the manifest
// only, one typed into a schema file was removed by the next run before
// anything had looked at it.
func TestDefinitiveMarkerTypedIntoTheYAMLIsHonoured(t *testing.T) {
	dir, cfg := writeStage(t, func(s *stage) {
		s.files["models/_stg.yml"] = "version: 2\nmodels:\n  - name: stg\n    columns:\n" +
			"      - name: ID\n        description: The wording we settled on.\n" +
			"        meta:\n          " + inherit.DefinitiveKey + ": true\n"
	})
	// Force would otherwise replace the local description with an ancestor's,
	// so this also proves the marker outranks it.
	cfg.Inheritance.Force = boolPtr(true)

	if _, err := runner.Run(cfg, runner.Options{Organize: true}); err != nil {
		t.Fatal(err)
	}
	body := readFile(t, dir, "models/_stg.yml")
	if !strings.Contains(body, inherit.DefinitiveKey) {
		t.Errorf("the marker was removed:\n%s", body)
	}
	if !strings.Contains(body, "The wording we settled on.") {
		t.Errorf("the settled wording was overwritten:\n%s", body)
	}
}

// `patch_path` can be stale in the other direction too: organising moves an
// entry and deletes the file it came from, while the manifest goes on naming
// that file. Reading it finds nothing, so every column looks new.
func TestASecondRunReportsNothing(t *testing.T) {
	work := copyFixture(t)
	cfg := fixtureConfig(work)

	first, err := runner.Run(cfg, runner.Options{Organize: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.FilesWritten) == 0 {
		t.Fatal("the first run wrote nothing, so there is nothing to be stable about")
	}

	second, err := runner.Run(cfg, runner.Options{Organize: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(second.FilesWritten) > 0 || len(second.FilesDeleted) > 0 {
		t.Errorf("the second run touched %v and deleted %v",
			second.FilesWritten, second.FilesDeleted)
	}
	// The count the summary line prints. Reporting edits against a file nothing
	// was written to is what made `-v` disagree with `git diff`.
	if len(second.Changes) > 0 {
		t.Errorf("the second run reported %d edits with nothing written:\n%v",
			len(second.Changes), second.Changes)
	}
}

// Every change the report lists must name a file the run actually touched.
func TestEveryReportedChangeNamesATouchedFile(t *testing.T) {
	work := copyFixture(t)
	rep, err := runner.Run(fixtureConfig(work), runner.Options{Organize: true})
	if err != nil {
		t.Fatal(err)
	}
	touched := map[string]bool{}
	for _, f := range append(append([]string{}, rep.FilesWritten...), rep.FilesDeleted...) {
		// FilesWritten is relative to the repository, Change.File to its project.
		touched[f] = true
		if i := strings.IndexByte(f, '/'); i >= 0 {
			touched[f[i+1:]] = true
		}
	}
	for _, c := range rep.Changes {
		if !touched[c.File] {
			t.Errorf("change %q names %s, which was neither written nor deleted",
				c.Detail, c.File)
		}
	}
}

// On a project documented before dbt-ditto arrived, the descriptions are in the
// manifest, so nothing is inherited and `inheritance.progenitor` had nothing to
// record — the setting looked inert on exactly the projects most likely to turn
// it on. A column already saying word for word what its parent says got that
// wording from that parent, and is annotated as such.
func TestProgenitorAnnotatesAWordingAnAncestorAlreadyHas(t *testing.T) {
	dir, cfg := writeStage(t, func(s *stage) {
		// The manifest knows the description, as it would after `dbt parse`, and
		// it is the seed's wording exactly.
		s.node(t, "model.demo.stg")["columns"] = map[string]any{
			"ID": map[string]any{"name": "ID", "description": "Identifier of the row."},
		}
		s.files["models/_stg.yml"] = "version: 2\nmodels:\n  - name: stg\n    columns:\n" +
			"      - name: ID\n        description: Identifier of the row.\n"
	})
	if _, err := runner.Run(cfg, runner.Options{Organize: true}); err != nil {
		t.Fatal(err)
	}
	block := columnEntry(t, readFile(t, dir, "models/_stg.yml"), "ID")
	if !strings.Contains(block, config.DefaultProgenitorKey+": seed.demo.raw") {
		t.Errorf("no progenitor recorded for a wording the seed already has:\n%s", block)
	}
}

// A description nobody upstream shares is the author's own, and claiming an
// ancestor for it would be a lie about where it came from.
func TestProgenitorLeavesAnOriginalWordingAlone(t *testing.T) {
	dir, cfg := writeStage(t, func(s *stage) {
		s.node(t, "model.demo.stg")["columns"] = map[string]any{
			"ID": map[string]any{"name": "ID", "description": "What this table means by an id."},
		}
		s.files["models/_stg.yml"] = "version: 2\nmodels:\n  - name: stg\n    columns:\n" +
			"      - name: ID\n        description: What this table means by an id.\n"
	})
	if _, err := runner.Run(cfg, runner.Options{Organize: true}); err != nil {
		t.Fatal(err)
	}
	block := columnEntry(t, readFile(t, dir, "models/_stg.yml"), "ID")
	if strings.Contains(block, config.DefaultProgenitorKey) {
		t.Errorf("a hand-written wording was attributed to an ancestor:\n%s", block)
	}
}
