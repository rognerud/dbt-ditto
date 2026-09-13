package runner_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rognerud/dbt-ditto/internal/config"
)

// The stage's `crm.orders` source is external: nothing in the project builds
// `demo.main.orders`, so it is the one node a provider has anything to say
// about, and every case here is written in terms of it.
const externalSource = "source.demo.crm.orders"

// sourceDoc is the cache entry a provider would have produced, written directly
// rather than spawned: these settings are about what happens to an answer, not
// how it was obtained.
func sourceDoc(columns ...map[string]any) map[string]any {
	return map[string]any{
		"unique_id":   externalSource,
		"description": "Orders as the CRM records them.",
		"labels":      map[string]string{"owner": "crm-team", "cost_centre": "1234"},
		"columns":     columns,
	}
}

func sourceColumn(name string, tweak ...func(map[string]any)) map[string]any {
	c := map[string]any{"name": name, "data_type": "BIGINT", "index": 1}
	for _, f := range tweak {
		f(c)
	}
	return c
}

func withLabels(kv map[string]string) func(map[string]any) {
	return func(c map[string]any) { c["labels"] = kv }
}

// writeSourceCache puts a provider answer where a run will read it.
func writeSourceCache(t *testing.T, dir string, docs ...map[string]any) {
	t.Helper()
	path := filepath.Join(dir, "target", "ditto-sources.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	body, err := json.MarshalIndent(map[string]any{
		"version": 1,
		"sources": docs,
	}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, path, string(body))
}

// stageWithSourceCache is the common build: the stage plus an answer about its
// one external source.
func stageWithSourceCache(docs ...map[string]any) func(*testing.T) (string, *config.Config) {
	return func(t *testing.T) (string, *config.Config) {
		t.Helper()
		dir := newStage().write(t, t.TempDir())
		writeSourceCache(t, dir, docs...)
		return dir, stageConfig(dir)
	}
}

// labelledColumn is `order_id` with a classification on it.
func labelledColumn() map[string]any {
	return sourceColumn("order_id", withLabels(map[string]string{"classification": "restricted"}))
}

func sourcesFile(t *testing.T, s snapshot) string { return s.file(t, "models/_sources.yml") }

func hasNote(s snapshot, substr string) bool {
	for _, n := range s.notes {
		if strings.Contains(n, substr) {
			return true
		}
	}
	return false
}

// echoProvider is a shell one-liner: it reads the request so nothing blocks on
// a pipe, and prints a fixed answer.
func echoProvider(t *testing.T, docs ...map[string]any) string {
	t.Helper()
	body, err := json.Marshal(map[string]any{"version": 1, "sources": docs})
	if err != nil {
		t.Fatal(err)
	}
	// Single-quoted for sh, with any embedded quote closed and reopened.
	quoted := strings.ReplaceAll(string(body), "'", `'\''`)
	return "cat >/dev/null; printf '%s' '" + quoted + "'"
}

func sourceSettingCases() []settingCase {
	return []settingCase{
		// --- providers -----------------------------------------------------
		{
			key:     "sources.providers[].command",
			claim:   "names a program that documents external sources, run on --refresh-sources",
			refresh: true,
			build: func(t *testing.T) (string, *config.Config) {
				// The `off` run has a provider that answers nothing, because a refresh with
				// none configured is an error rather than a run that quietly does less.
				return providerStage(t, echoProvider(t))
			},
			set: func(t *testing.T, c *config.Config, dir string) {
				c.Sources.Providers[0].Command = echoProvider(t, sourceDoc(sourceColumn("order_id")))
			},
			off: func(t *testing.T, s snapshot) {
				lacks(t, sourcesFile(t, s), "Orders as the CRM records them.",
					"nothing should document the source with no provider configured")
			},
			on: func(t *testing.T, s snapshot) {
				contains(t, sourcesFile(t, s), "Orders as the CRM records them.",
					"the provider's answer did not reach the source YAML")
			},
		},
		{
			key:     "sources.providers[].match.database",
			claim:   "limits a provider to sources in matching databases",
			refresh: true,
			build: func(t *testing.T) (string, *config.Config) {
				return providerStage(t, echoProvider(t, sourceDoc(sourceColumn("order_id"))))
			},
			set: func(t *testing.T, c *config.Config, dir string) {
				// The stage's source lives in `demo`, so this claims nothing.
				c.Sources.Providers[0].Match.Database = "elsewhere"
			},
			off: func(t *testing.T, s snapshot) {
				contains(t, sourcesFile(t, s), "Orders as the CRM records them.",
					"an unrestricted provider should answer for every source")
			},
			on: func(t *testing.T, s snapshot) {
				lacks(t, sourcesFile(t, s), "Orders as the CRM records them.",
					"a provider that claims no database should not have been asked")
			},
		},
		{
			key:     "sources.providers[].match.schema",
			claim:   "limits a provider to sources in matching schemas",
			refresh: true,
			build: func(t *testing.T) (string, *config.Config) {
				return providerStage(t, echoProvider(t, sourceDoc(sourceColumn("order_id"))))
			},
			set: func(t *testing.T, c *config.Config, dir string) {
				c.Sources.Providers[0].Match.Schema = "not_main"
			},
			off: func(t *testing.T, s snapshot) {
				contains(t, sourcesFile(t, s), "Orders as the CRM records them.",
					"an unrestricted provider should answer for every source")
			},
			on: func(t *testing.T, s snapshot) {
				lacks(t, sourcesFile(t, s), "Orders as the CRM records them.",
					"a provider that claims no schema should not have been asked")
			},
		},
		{
			key:     "sources.strict",
			claim:   "fails the run when a source provider does, instead of carrying on",
			refresh: true,
			fail:    true,
			build: func(t *testing.T) (string, *config.Config) {
				return providerStage(t, "cat >/dev/null; echo 'no credentials for bq-project' >&2; exit 3")
			},
			set: func(t *testing.T, c *config.Config, dir string) { c.Sources.Strict = ptrTo(true) },
			off: func(t *testing.T, s snapshot) {
				// Without strict the run finishes and says what went wrong.
				s.file(t, "models/_sources.yml")
				if !hasNote(s, "no credentials") {
					t.Errorf("a provider that failed should still be reported, got %v", s.notes)
				}
			},
			on: func(t *testing.T, s snapshot) {},
		},
		{
			key:   "sources.cache",
			claim: "reads and writes the provider answer somewhere other than target/",
			build: func(t *testing.T) (string, *config.Config) {
				dir := newStage().write(t, t.TempDir())
				// Only the non-default location has an answer, so the setting decides whether
				// the source is documented at all.
				path := filepath.Join(dir, "ci", "sources.json")
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				body, err := json.Marshal(map[string]any{
					"version": 1,
					"sources": []map[string]any{sourceDoc(sourceColumn("order_id"))},
				})
				if err != nil {
					t.Fatal(err)
				}
				writeFile(t, path, string(body))
				return dir, stageConfig(dir)
			},
			set: func(t *testing.T, c *config.Config, dir string) {
				c.Sources.Cache = ptrTo("ci/sources.json")
			},
			off: func(t *testing.T, s snapshot) {
				lacks(t, sourcesFile(t, s), "Orders as the CRM records them.",
					"the default cache location is empty, so nothing should be documented")
			},
			on: func(t *testing.T, s snapshot) {
				contains(t, sourcesFile(t, s), "Orders as the CRM records them.",
					"the configured cache was not read")
			},
		},

		// --- label routing -------------------------------------------------
		{
			key:   "sources.labels.mode",
			claim: "routes provider-reported labels into meta, tags, both or nowhere",
			build: stageWithSourceCache(sourceDoc(labelledColumn())),
			set: func(t *testing.T, c *config.Config, dir string) {
				c.Sources.Labels.Mode = ptrTo(config.LabelsTags)
			},
			off: func(t *testing.T, s snapshot) {
				contains(t, sourcesFile(t, s), "classification: restricted",
					"labels should land in meta by default")
			},
			on: func(t *testing.T, s snapshot) {
				contains(t, sourcesFile(t, s), "classification:restricted",
					"tags mode should render the pair as a dbt tag")
			},
		},
		{
			key:   "sources.labels.meta_key",
			claim: "nests provider labels under a named meta key, or flattens them when empty",
			build: stageWithSourceCache(sourceDoc(labelledColumn())),
			set: func(t *testing.T, c *config.Config, dir string) {
				c.Sources.Labels.MetaKey = ptrTo("warehouse_labels")
			},
			off: func(t *testing.T, s snapshot) {
				contains(t, sourcesFile(t, s), "labels:", "the default key should namespace the labels")
			},
			on: func(t *testing.T, s snapshot) {
				contains(t, sourcesFile(t, s), "warehouse_labels:", "the configured meta key was not used")
			},
		},
		{
			key:   "sources.labels.tag_format",
			claim: "controls how a label pair is rendered as a tag",
			// Both runs route to tags, since the format is only observable once rendered.
			build: func(t *testing.T) (string, *config.Config) {
				dir, cfg := stageWithSourceCache(sourceDoc(labelledColumn()))(t)
				cfg.Sources.Labels.Mode = ptrTo(config.LabelsTags)
				return dir, cfg
			},
			set: func(t *testing.T, c *config.Config, dir string) {
				c.Sources.Labels.TagFormat = ptrTo("{key}={value}")
			},
			off: func(t *testing.T, s snapshot) {
				contains(t, sourcesFile(t, s), "classification:restricted", "the default format is key:value")
			},
			on: func(t *testing.T, s snapshot) {
				contains(t, sourcesFile(t, s), "classification=restricted", "the configured format was not used")
			},
		},
		{
			key:   "sources.labels.include",
			claim: "keeps only the label keys listed, dropping the rest",
			build: stageWithSourceCache(sourceDoc(sourceColumn("order_id",
				withLabels(map[string]string{"classification": "restricted", "terraform_stack": "crm"})))),
			set: func(t *testing.T, c *config.Config, dir string) {
				c.Sources.Labels.Include = []string{"classification"}
			},
			off: func(t *testing.T, s snapshot) {
				contains(t, sourcesFile(t, s), "terraform_stack:", "every label should be kept by default")
			},
			on: func(t *testing.T, s snapshot) {
				contains(t, sourcesFile(t, s), "classification:", "the included key should still be written")
				lacks(t, sourcesFile(t, s), "terraform_stack:", "a key not on the include list should be dropped")
			},
		},
		{
			key:   "sources.labels.exclude",
			claim: "drops the label keys listed",
			build: stageWithSourceCache(sourceDoc(sourceColumn("order_id",
				withLabels(map[string]string{"classification": "restricted", "terraform_stack": "crm"})))),
			set: func(t *testing.T, c *config.Config, dir string) {
				c.Sources.Labels.Exclude = []string{"terraform_*"}
			},
			off: func(t *testing.T, s snapshot) {
				contains(t, sourcesFile(t, s), "terraform_stack:", "every label should be kept by default")
			},
			on: func(t *testing.T, s snapshot) {
				contains(t, sourcesFile(t, s), "classification:", "an unexcluded key should still be written")
				lacks(t, sourcesFile(t, s), "terraform_stack:", "an excluded key should be dropped")
			},
		},

		// --- propagation ---------------------------------------------------
		{
			key:   "sources.labels.propagate.column",
			claim: "carries a source column's labels downstream with the column",
			build: stageWithSourceCache(sourceDoc(labelledColumn())),
			set: func(t *testing.T, c *config.Config, dir string) {
				c.Sources.Labels.Propagate.Column = ptrTo(false)
			},
			off: func(t *testing.T, s snapshot) {
				contains(t, s.file(t, "models/_stg_orders.yml"), "classification: restricted",
					"a column label should reach the model that reads the source")
			},
			on: func(t *testing.T, s snapshot) {
				contains(t, sourcesFile(t, s), "classification: restricted",
					"the source itself should still carry its own labels")
				lacks(t, s.file(t, "models/_stg_orders.yml"), "classification: restricted",
					"propagation is off, so the label should have stopped at the source")
			},
		},
		{
			key:   "sources.labels.propagate.structs",
			claim: "carries labels across a struct pack or unpack match, where the data is unchanged",
			build: func(t *testing.T) (string, *config.Config) {
				dir, cfg := stageWithPackedSourceColumn(t)
				cfg.Inheritance.Derived.Enabled = ptrTo(true)
				return dir, cfg
			},
			set: func(t *testing.T, c *config.Config, dir string) {
				c.Sources.Labels.Propagate.Structs = ptrTo(false)
			},
			off: func(t *testing.T, s snapshot) {
				contains(t, s.file(t, "models/_packed.yml"), "classification: restricted",
					"the same bytes in a different shape should keep the classification")
			},
			on: func(t *testing.T, s snapshot) {
				lacks(t, s.file(t, "models/_packed.yml"), "classification: restricted",
					"struct propagation is off, so the label should not have travelled")
			},
		},
		{
			key:   "sources.labels.propagate.aggregates",
			claim: "decides whether an aggregated column inherits labels, warns, or is silent",
			build: func(t *testing.T) (string, *config.Config) {
				dir, cfg := stageWithAggregatedSourceColumn(t)
				cfg.Inheritance.Derived.Enabled = ptrTo(true)
				return dir, cfg
			},
			set: func(t *testing.T, c *config.Config, dir string) {
				c.Sources.Labels.Propagate.Aggregates = ptrTo(config.AggregatesInherit)
			},
			off: func(t *testing.T, s snapshot) {
				lacks(t, s.file(t, "models/_agg.yml"), "classification: restricted",
					"aggregating a value does not preserve what was said about it")
				if !hasWarning(s, "labels were not carried across") {
					t.Errorf("dropping a classification silently is the dangerous case; wanted a warning, got %v", s.warnings)
				}
			},
			on: func(t *testing.T, s snapshot) {
				contains(t, s.file(t, "models/_agg.yml"), "classification: restricted",
					"inherit was asked for, so the label should have travelled")
			},
		},
		{
			key:   "sources.labels.propagate.on_conflict",
			claim: "decides what happens when one generation labels a column two ways",
			build: func(t *testing.T) (string, *config.Config) {
				return stageWithConflictingLabels(t)
			},
			set: func(t *testing.T, c *config.Config, dir string) {
				c.Sources.Labels.Propagate.OnConflict = ptrTo(config.ConflictFirst)
			},
			off: func(t *testing.T, s snapshot) {
				if !hasWarning(s, "label this column differently") {
					t.Errorf("a disagreement about a classification should be reported, got %v", s.warnings)
				}
				lacks(t, s.file(t, "models/_both.yml"), "classification:",
					"nothing should be written when the ancestors disagree")
			},
			on: func(t *testing.T, s snapshot) {
				contains(t, s.file(t, "models/_both.yml"), "classification:",
					"first was asked for, so the lowest unique_id should have won")
			},
		},

		// --- extra keys ----------------------------------------------------
		{
			key:   "inheritance.extra_keys",
			claim: "carries column keys this tool does not model, such as policy_tags, down the DAG",
			build: stageBuild(func(t *testing.T, s *stage) {
				// dbt writes policy_tags on the column itself, and nothing here understands it.
				s.col(t, "seed.demo.raw", "id")["policy_tags"] = []string{"taxonomies/1/policyTags/2"}
			}),
			set: func(t *testing.T, c *config.Config, dir string) {
				c.Inheritance.ExtraKeys = []string{"policy_tags"}
			},
			off: func(t *testing.T, s snapshot) {
				lacks(t, stgFile(t, s), "policy_tags",
					"an unlisted key should not be carried: decoding every unknown key is not free")
			},
			on: func(t *testing.T, s snapshot) {
				contains(t, stgFile(t, s), "policy_tags",
					"the named key should have reached the downstream column")
			},
		},
	}
}
