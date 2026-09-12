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

// The fixture contains a real disagreement: stg_orders_enriched reads both a
// seed and a CRM source, and the two document `order_id` and `status`
// differently. Which one wins is decided by unique_id order, so the run says so.
func TestAmbiguousColumnsInTheFixtureAreReported(t *testing.T) {
	work := copyFixture(t)

	rep, err := runner.Run(fixtureConfig(work), runner.Options{DryRun: true, Organize: true})
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	want := map[string]bool{
		"model.platform.stg_orders_enriched.order_id": false,
		"model.platform.stg_orders_enriched.status":   false,
	}
	for _, w := range rep.Warnings {
		if w.Kind != inherit.WarnAmbiguous {
			t.Errorf("unexpected %s warning: %+v", w.Kind, w)
			continue
		}
		key := w.Node + "." + w.Column
		if _, expected := want[key]; !expected {
			t.Errorf("unexpected ambiguity warning for %s: %s", key, w.Detail)
			continue
		}
		want[key] = true
		if !strings.Contains(w.Detail, "source.platform.crm.raw_orders") ||
			!strings.Contains(w.Detail, "took seed.platform.raw_orders") {
			t.Errorf("%s: detail = %q, want both parents named", key, w.Detail)
		}
	}
	for key, seen := range want {
		if !seen {
			t.Errorf("%s: no ambiguity warning, but its parents disagree", key)
		}
	}
}

// A warning scrolls away; the annotation stays beside the description it
// qualifies. `ambiguity_meta` writes the dissenting ancestors into the column's
// meta, and a second run leaves the same annotation rather than churning it.
func TestAmbiguityMetaAnnotatesTheColumn(t *testing.T) {
	work := copyFixture(t)
	cfg := fixtureConfig(work)
	cfg.Inheritance.AmbiguityMeta = boolPtr(true)

	if _, err := runner.Run(cfg, runner.Options{Organize: true}); err != nil {
		t.Fatalf("first run: %v", err)
	}

	path := filepath.Join(work, "platform", "models", "staging", "_stg_orders_enriched.yml")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	got := string(body)
	if !strings.Contains(got, config.DefaultAmbiguityKey) {
		t.Fatalf("no %s annotation written:\n%s", config.DefaultAmbiguityKey, got)
	}
	if !strings.Contains(got, "source.platform.crm.raw_orders") {
		t.Errorf("annotation does not name the dissenting ancestor:\n%s", got)
	}

	rep, err := runner.Run(cfg, runner.Options{Check: true, DryRun: true, Organize: true})
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if len(rep.FilesWritten) > 0 {
		t.Errorf("second run would rewrite %v, want the annotation to be stable", rep.FilesWritten)
	}
}

// Turning the annotation off has to take the annotation with it: a key left in
// the file would keep claiming a disagreement nobody is being told about.
// (Clearing it for a column that stopped inheriting is covered at the resolver
// level, in inherit/ambiguity_test.go, because the description a column counts
// as "its own" comes from the manifest rather than from the file on disk.)
func TestAmbiguityMetaIsRemovedWhenTurnedOff(t *testing.T) {
	work := copyFixture(t)
	cfg := fixtureConfig(work)
	cfg.Inheritance.AmbiguityMeta = boolPtr(true)

	if _, err := runner.Run(cfg, runner.Options{Organize: true}); err != nil {
		t.Fatalf("first run: %v", err)
	}
	path := filepath.Join(work, "platform", "models", "staging", "_stg_orders_enriched.yml")
	if body, err := os.ReadFile(path); err != nil {
		t.Fatal(err)
	} else if !strings.Contains(string(body), config.DefaultAmbiguityKey) {
		t.Fatalf("nothing to remove: no annotation written:\n%s", body)
	}

	cfg.Inheritance.AmbiguityMeta = boolPtr(false)
	if _, err := runner.Run(cfg, runner.Options{Organize: true}); err != nil {
		t.Fatalf("second run: %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(after), config.DefaultAmbiguityKey) {
		t.Errorf("annotation survived being turned off:\n%s", after)
	}
}

// Turning the warnings off must not take the annotation with it: they are two
// ways of saying the same thing, to two different readers.
func TestAmbiguityMetaWorksWithWarningsOff(t *testing.T) {
	work := copyFixture(t)
	cfg := fixtureConfig(work)
	cfg.Inheritance.AmbiguityMeta = boolPtr(true)
	cfg.Inheritance.WarnAmbiguous = boolPtr(false)

	rep, err := runner.Run(cfg, runner.Options{Organize: true})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	for _, w := range rep.Warnings {
		if w.Kind == inherit.WarnAmbiguous {
			t.Errorf("warn_ambiguous is off but an ambiguity warning was reported: %+v", w)
		}
	}
	path := filepath.Join(work, "platform", "models", "staging", "_stg_orders_enriched.yml")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if !strings.Contains(string(body), config.DefaultAmbiguityKey) {
		t.Errorf("no annotation written with warnings off:\n%s", body)
	}
}

func boolPtr(b bool) *bool { return &b }

// Warnings are about the project, not about what a run wrote, so a second run
// over already-inherited YAML still reports them.
func TestWarningsSurviveASecondRun(t *testing.T) {
	work := copyFixture(t)

	if _, err := runner.Run(fixtureConfig(work), runner.Options{Organize: true}); err != nil {
		t.Fatalf("first run: %v", err)
	}
	rep, err := runner.Run(fixtureConfig(work), runner.Options{Organize: true})
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if len(rep.Warnings) == 0 {
		t.Error("no warnings on the second run, want the disagreement still reported")
	}
}
