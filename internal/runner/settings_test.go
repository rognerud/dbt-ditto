package runner_test

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/rognerud/dbt-ditto/internal/config"
	"github.com/rognerud/dbt-ditto/internal/runner"
)

// Every setting gets one case here, and a case has to prove two things: that
// changing the setting changes the run at all, and that it changes it in the
// way the documentation claims. `off` sees the run with the setting left alone,
// `on` sees it with the setting changed, and the two snapshots must differ.
//
// TestEverySettingIsCovered walks the config structs and fails if a key has no
// case, so a setting cannot be added without one.
type settingCase struct {
	// key is the dotted path in dbt_ditto.yml, and is what the coverage guard
	// matches against.
	key string
	// claim is what the documentation says this setting does, in one line.
	claim string
	// build lays out the project and returns the config the `off` run uses.
	build func(t *testing.T) (dir string, cfg *config.Config)
	// set changes the setting under test, and nothing else.
	set func(t *testing.T, c *config.Config, dir string)
	off check
	on  check

	// refresh runs the source providers, which is the only way a provider is
	// ever spawned: an ordinary run reads the cache and connects to nothing.
	refresh bool
	// fail marks a setting whose whole claim is that the run stops.
	fail bool
}

// check is one half of a case: an assertion about a finished run.
type check func(t *testing.T, s snapshot)

// stageBuild is the default: the shared stage, tweaked by fn.
func stageBuild(fn func(t *testing.T, s *stage)) func(*testing.T) (string, *config.Config) {
	return func(t *testing.T) (string, *config.Config) {
		t.Helper()
		s := newStage()
		if fn != nil {
			fn(t, s)
		}
		dir := s.write(t, t.TempDir())
		return dir, stageConfig(dir)
	}
}

// stageBuildCfg is stageBuild with the `off` config pre-adjusted, for settings
// that only mean anything once a feature is switched on.
func stageBuildCfg(fn func(t *testing.T, s *stage), cfgFn func(*config.Config)) func(*testing.T) (string, *config.Config) {
	inner := stageBuild(fn)
	return func(t *testing.T) (string, *config.Config) {
		dir, cfg := inner(t)
		cfgFn(cfg)
		return dir, cfg
	}
}

func contains(t *testing.T, body, want, why string) {
	t.Helper()
	if !strings.Contains(body, want) {
		t.Errorf("%s\nwanted %q in:\n%s", why, want, body)
	}
}

func lacks(t *testing.T, body, unwanted, why string) {
	t.Helper()
	if strings.Contains(body, unwanted) {
		t.Errorf("%s\ndid not want %q in:\n%s", why, unwanted, body)
	}
}

func stgFile(t *testing.T, s snapshot) string { return s.file(t, "models/_stg.yml") }

// has and lacksIn are the two assertions nearly every case makes, about the
// staging model's schema file; inFile and notInFile name another file, and
// inCol and notInCol narrow to a single column entry so an assertion cannot
// accidentally match text belonging to its neighbour.
func has(want, why string) check {
	return func(t *testing.T, s snapshot) { t.Helper(); contains(t, stgFile(t, s), want, why) }
}

func lacksIn(unwanted, why string) check {
	return func(t *testing.T, s snapshot) { t.Helper(); lacks(t, stgFile(t, s), unwanted, why) }
}

func inFile(path, want, why string) check {
	return func(t *testing.T, s snapshot) { t.Helper(); contains(t, s.file(t, path), want, why) }
}

func notInFile(path, unwanted, why string) check {
	return func(t *testing.T, s snapshot) { t.Helper(); lacks(t, s.file(t, path), unwanted, why) }
}

func inCol(column, want, why string) check {
	return func(t *testing.T, s snapshot) { t.Helper(); contains(t, colBlock(t, s, column), want, why) }
}

func notInCol(column, unwanted, why string) check {
	return func(t *testing.T, s snapshot) { t.Helper(); lacks(t, colBlock(t, s, column), unwanted, why) }
}

// both runs two checks, for the cases that assert more than one thing.
func both(a, b check) check {
	return func(t *testing.T, s snapshot) { t.Helper(); a(t, s); b(t, s) }
}

func settingCases() []settingCase {
	cases := []settingCase{
		// --- projects ------------------------------------------------------
		{
			key:   "projects[].path",
			claim: "names the dbt project directory to read and write",
			build: func(t *testing.T) (string, *config.Config) {
				root := t.TempDir()
				a := newStage().write(t, filepath.Join(root, "a"))
				other := newStage()
				other.col(t, "seed.demo.raw", "id")["description"] = "The other project's wording."
				other.write(t, filepath.Join(root, "b"))
				return a, &config.Config{Dir: root, Projects: []config.ProjectRef{{Path: a}}}
			},
			set: func(t *testing.T, c *config.Config, dir string) {
				c.Projects[0].Path = filepath.Join(filepath.Dir(dir), "b")
			},
			off: has("Identifier of the row.", "the configured project was not the one read"),
			// The other project was documented instead; this one was left exactly
			// as it was written out.
			on: lacksIn("Identifier of the row.",
				"the project the path no longer points at was still written"),
		},
		{
			key:   "projects[].target",
			claim: "reads the artifacts from somewhere other than <path>/target",
			build: func(t *testing.T) (string, *config.Config) {
				s := newStage()
				dir := s.write(t, t.TempDir())
				// A second set of artifacts, as CI would download them,
				// documenting the same column differently.
				alt := newStage()
				alt.col(t, "seed.demo.raw", "id")["description"] = "Documented in the artifacts CI downloaded."
				alt.writeArtifacts(t, filepath.Join(dir, "ci_artifacts"))
				return dir, stageConfig(dir)
			},
			set: func(t *testing.T, c *config.Config, dir string) { c.Projects[0].Target = "ci_artifacts" },
			off: has("Identifier of the row.", "the default target/ was not read"),
			on: has("Documented in the artifacts CI downloaded.",
				"the configured target directory was not read"),
		},
		{
			key:   "projects[].manifest",
			claim: "adds an upstream project that is only a manifest file, with no checkout",
			build: func(t *testing.T) (string, *config.Config) {
				root := t.TempDir()
				// The downstream project on its own: its seed is gone, so
				// nothing documents `ID` until the upstream manifest is added.
				down := newStage()
				delete(down.nodes, "seed.demo.raw")
				delete(down.nodes, "seed.demo.raw_alt")
				down.node(t, "model.demo.stg")["depends_on"] = map[string]any{
					"nodes": []string{"seed.upstream.raw"},
				}
				dir := down.write(t, filepath.Join(root, "downstream"))
				upstreamManifest(t, filepath.Join(root, "upstream"),
					"Documented in a manifest nobody checked out.")
				return dir, &config.Config{Dir: root, Projects: []config.ProjectRef{{Path: dir}}}
			},
			set: func(t *testing.T, c *config.Config, dir string) {
				c.Projects = append(c.Projects, config.ProjectRef{
					Manifest: filepath.Join(filepath.Dir(dir), "upstream", "manifest.json"),
					Upstream: true,
				})
			},
			off: lacksIn("Documented in a manifest nobody checked out.",
				"documentation appeared without the manifest being configured"),
			on: has("Documented in a manifest nobody checked out.",
				"the manifest-only project did not donate its documentation"),
		},
		{
			key:   "projects[].upstream",
			claim: "loads a project for its metadata only, and never writes its YAML",
			build: func(t *testing.T) (string, *config.Config) {
				root := t.TempDir()
				a := newStage().write(t, filepath.Join(root, "a"))
				return a, &config.Config{Dir: root, Projects: []config.ProjectRef{{Path: a}}}
			},
			set: func(t *testing.T, c *config.Config, dir string) { c.Projects[0].Upstream = true },
			off: has("Identifier of the row.", "a writable project was not written"),
			on: both(lacksIn("Identifier of the row.", "an upstream project was written to"),
				func(t *testing.T, s snapshot) {
					if s.scanned != 0 {
						t.Errorf("%d nodes scanned, want none: an upstream project has nothing to write", s.scanned)
					}
				}),
		},
		{
			key:   "loom",
			claim: "reads dbt_loom.config.yml for upstream manifests; false ignores it",
			build: func(t *testing.T) (string, *config.Config) {
				root := t.TempDir()
				down := newStage()
				delete(down.nodes, "seed.demo.raw")
				delete(down.nodes, "seed.demo.raw_alt")
				down.node(t, "model.demo.stg")["depends_on"] = map[string]any{
					"nodes": []string{"seed.upstream.raw"},
				}
				down.raw["dbt_loom.config.yml"] = "manifests:\n  - name: upstream\n    type: file\n" +
					"    config:\n      path: ../upstream/manifest.json\n"
				dir := down.write(t, filepath.Join(root, "downstream"))
				upstreamManifest(t, filepath.Join(root, "upstream"),
					"Documented in the manifest dbt-loom points at.")

				// Loaded from a real config file, because reading dbt-loom's
				// config is part of loading and not something a caller does.
				return dir, loadConfig(t, root, "projects:\n  - path: downstream\n")
			},
			set: func(t *testing.T, c *config.Config, dir string) {
				*c = *loadConfig(t, filepath.Dir(dir), "loom: false\nprojects:\n  - path: downstream\n")
			},
			off: has("Documented in the manifest dbt-loom points at.",
				"the dbt-loom config was not read"),
			on: lacksIn("Documented in the manifest dbt-loom points at.",
				"the dbt-loom config was read with loom: false"),
		},

		// --- inheritance ---------------------------------------------------
		{
			key:   "inheritance.columns",
			claim: "inherit column documentation at all",
			build: stageBuild(nil),
			set:   func(_ *testing.T, c *config.Config, _ string) { c.Inheritance.Columns = ptrTo(false) },
			off:   has("Identifier of the row.", "columns did not inherit"),
			on: lacksIn("Identifier of the row.",
				"a description was inherited with inheritance.columns off"),
		},
		{
			key:   "inheritance.node_description",
			claim: "inherit the model's own description, not just its columns'",
			build: stageBuild(func(t *testing.T, s *stage) {
				s.node(t, "seed.demo.raw")["description"] = "What the table as a whole holds."
			}),
			set: func(_ *testing.T, c *config.Config, _ string) { c.Inheritance.NodeDescription = ptrTo(true) },
			off: lacksIn("What the table as a whole holds.",
				"the node description was inherited by default"),
			on: has("What the table as a whole holds.", "the node description was not inherited"),
		},
		{
			key:   "inheritance.meta",
			claim: "merge a column's meta down the DAG",
			build: stageBuild(nil),
			set:   func(_ *testing.T, c *config.Config, _ string) { c.Inheritance.Meta = ptrTo(false) },
			off:   has("owner: platform", "meta did not inherit"),
			on:    lacksIn("owner: platform", "meta inherited with inheritance.meta off"),
		},
		{
			key:   "inheritance.tags",
			claim: "merge a column's tags down the DAG",
			build: stageBuild(nil),
			set:   func(_ *testing.T, c *config.Config, _ string) { c.Inheritance.Tags = ptrTo(false) },
			off:   has("- core", "tags did not inherit"),
			on:    lacksIn("- core", "tags inherited with inheritance.tags off"),
		},
		{
			key:   "inheritance.case_insensitive",
			claim: "match a column to an upstream one spelled in another case",
			build: stageBuild(nil),
			set:   func(_ *testing.T, c *config.Config, _ string) { c.Inheritance.CaseInsensitive = ptrTo(false) },
			// The model's column is `ID`, the seed documents `id`.
			off: has("Identifier of the row.", "ID did not match the upstream id"),
			on:  lacksIn("Identifier of the row.", "ID matched id with case-insensitive matching off"),
		},
		{
			key:   "inheritance.force",
			claim: "overwrite a description the column already has",
			// ID is documented both here and upstream, which is the only case
			// force is about.
			build: stageBuild(func(t *testing.T, s *stage) {
				s.col(t, "model.demo.stg", "ID")["description"] = "The local wording."
				s.files["models/_stg.yml"] = strings.Replace(s.files["models/_stg.yml"],
					"      - name: ID\n",
					"      - name: ID\n        description: The local wording.\n", 1)
			}),
			set: func(_ *testing.T, c *config.Config, _ string) { c.Inheritance.Force = ptrTo(true) },
			off: has("The local wording.", "a hand-written description was replaced without force"),
			on: both(has("Identifier of the row.", "force did not overwrite the local description"),
				lacksIn("The local wording.", "the local description survived force")),
		},
		{
			key:   "inheritance.placeholders",
			claim: "upstream descriptions treated as no description at all",
			build: stageBuild(func(t *testing.T, s *stage) {
				s.col(t, "seed.demo.raw", "amount_cents")["description"] = "TBC"
			}),
			set: func(_ *testing.T, c *config.Config, _ string) {
				c.Inheritance.Placeholders = []string{"TBC"}
			},
			off: has("TBC", "an ordinary description was dropped"),
			on:  lacksIn("TBC", "a configured placeholder was still inherited"),
		},
		{
			key:   "inheritance.skip_meta_keys",
			claim: "meta keys that are never inherited",
			build: stageBuild(nil),
			set: func(_ *testing.T, c *config.Config, _ string) {
				c.Inheritance.SkipMetaKeys = []string{"owner"}
			},
			off: has("owner: platform", "meta did not inherit"),
			on: both(lacksIn("owner: platform", "a skipped meta key was inherited"),
				has("pii: true", "skipping one key dropped the others too")),
		},
		{
			key:   "inheritance.progenitor",
			claim: "record which node an inherited description came from",
			build: stageBuild(nil),
			set:   func(_ *testing.T, c *config.Config, _ string) { c.Inheritance.Progenitor = ptrTo(false) },
			off:   has("osmosis_progenitor: seed.demo.raw", "no progenitor was recorded"),
			on:    lacksIn("osmosis_progenitor", "a progenitor was recorded anyway"),
		},
		{
			key:   "inheritance.progenitor_key",
			claim: "the meta key the progenitor is recorded under",
			build: stageBuild(nil),
			set: func(_ *testing.T, c *config.Config, _ string) {
				c.Inheritance.ProgenitorKey = ptrTo("came_from")
			},
			off: has("osmosis_progenitor:", "the default key was not used"),
			on: both(has("came_from: seed.demo.raw", "the configured key was not used"),
				lacksIn("osmosis_progenitor", "the default key was written as well")),
		},
		{
			key:   "inheritance.directives",
			claim: `honour a description written as "Inherited: node.column"`,
			build: stageBuild(withDirective("Inherited: raw.first_name")),
			set:   func(_ *testing.T, c *config.Config, _ string) { c.Inheritance.Directives = ptrTo(false) },
			off:   has("Given name, as the customer typed it.", "the directive was not followed"),
			on:    has("Inherited: raw.first_name", "the directive was followed with directives off"),
		},
		{
			key:   "inheritance.directive_prefix",
			claim: "the marker that turns a description into a pointer",
			build: stageBuild(withDirective("See: raw.first_name")),
			set: func(_ *testing.T, c *config.Config, _ string) {
				c.Inheritance.DirectivePrefix = ptrTo("See:")
			},
			off: has("See: raw.first_name", "a description was treated as a directive by the wrong marker"),
			on:  has("Given name, as the customer typed it.", "the configured marker was not recognised"),
		},
		{
			key:   "inheritance.warn_ambiguous",
			claim: "report on stderr when two parents document a column differently",
			build: stageBuild(nil),
			set:   func(_ *testing.T, c *config.Config, _ string) { c.Inheritance.WarnAmbiguous = ptrTo(false) },
			off: func(t *testing.T, s snapshot) {
				if !hasWarning(s, "documented differently by seed.demo.raw_alt") {
					t.Errorf("no ambiguity warning; got %v", s.warnings)
				}
			},
			on: func(t *testing.T, s snapshot) {
				if hasWarning(s, "documented differently") {
					t.Errorf("ambiguity warned about with warn_ambiguous off: %v", s.warnings)
				}
			},
		},
		{
			key:   "inheritance.ambiguity_meta",
			claim: "record that disagreement in the column's meta",
			build: stageBuild(nil),
			set:   func(_ *testing.T, c *config.Config, _ string) { c.Inheritance.AmbiguityMeta = ptrTo(true) },
			off:   lacksIn(config.DefaultAmbiguityKey, "the annotation was written by default"),
			on: both(has(config.DefaultAmbiguityKey, "no annotation was written"),
				has("- seed.demo.raw_alt", "the annotation did not name the dissenter")),
		},
		{
			key:   "inheritance.ambiguity_key",
			claim: "the meta key the disagreement is recorded under",
			build: stageBuildCfg(nil, func(c *config.Config) {
				c.Inheritance.AmbiguityMeta = ptrTo(true)
			}),
			set: func(_ *testing.T, c *config.Config, _ string) {
				c.Inheritance.AmbiguityKey = ptrTo("disputed_by")
			},
			off: has(config.DefaultAmbiguityKey+":", "the default key was not used"),
			on: both(has("disputed_by:", "the configured key was not used"),
				lacksIn(config.DefaultAmbiguityKey, "the default key was written as well")),
		},
		{
			key:   "inheritance.derived.enabled",
			claim: "match a column to an upstream one it no longer shares a name with",
			build: stageBuild(withDerivedColumns),
			set:   func(_ *testing.T, c *config.Config, _ string) { c.Inheritance.Derived.Enabled = ptrTo(true) },
			off: both(
				lacksIn("Amount, in cents.\n        name: total_amount_cents", "derived matching ran by default"),
				notInCol("total_amount_cents", "Amount, in cents.", "an aggregated column inherited by default")),
			on: inCol("total_amount_cents", "Amount, in cents.", "the aggregated column did not inherit"),
		},
		{
			key:   "inheritance.derived.structs",
			claim: "match a struct field to the flat column it was packed from",
			build: stageBuildCfg(withDerivedColumns, enableDerived),
			set:   func(_ *testing.T, c *config.Config, _ string) { c.Inheritance.Derived.Structs = ptrTo(false) },
			off: inCol("profile.first_name", "Given name, as the customer typed it.",
				"the struct field did not inherit from the flat column"),
			on: notInCol("profile.first_name", "Given name, as the customer typed it.",
				"the struct field matched with derived.structs off"),
		},
		{
			key:   "inheritance.derived.aggregates",
			claim: "match an aggregated column to the column it was computed from",
			build: stageBuildCfg(withDerivedColumns, enableDerived),
			set:   func(_ *testing.T, c *config.Config, _ string) { c.Inheritance.Derived.Aggregates = ptrTo(false) },
			off:   inCol("total_amount_cents", "Amount, in cents.", "the aggregated column did not inherit"),
			on: notInCol("total_amount_cents", "Amount, in cents.",
				"the aggregate matched with derived.aggregates off"),
		},
		{
			key:   "inheritance.derived.prefixes",
			claim: "the aggregate words stripped from the front of a column name",
			build: stageBuildCfg(withDerivedColumns, enableDerived),
			set: func(_ *testing.T, c *config.Config, _ string) {
				c.Inheritance.Derived.Prefixes = []string{"grand"}
			},
			off: inCol("total_amount_cents", "Amount, in cents.", "`total` is a default prefix and did not match"),
			on: notInCol("total_amount_cents", "Amount, in cents.",
				"`total` still matched after the prefix list was replaced"),
		},
		{
			key:   "inheritance.derived.suffixes",
			claim: "the aggregate words stripped from the end of a column name",
			build: stageBuildCfg(withDerivedColumns, enableDerived),
			set: func(_ *testing.T, c *config.Config, _ string) {
				c.Inheritance.Derived.Suffixes = []string{"grand"}
			},
			off: inCol("amount_cents_sum", "Amount, in cents.", "`sum` is a default suffix and did not match"),
			on: notInCol("amount_cents_sum", "Amount, in cents.",
				"`sum` still matched after the suffix list was replaced"),
		},
		{
			key:   "inheritance.backfill.enabled",
			claim: "take documentation up the DAG, from a node's descendants",
			build: stageBuild(nil),
			set:   func(_ *testing.T, c *config.Config, _ string) { c.Inheritance.Backfill.Enabled = ptrTo(true) },
			off: notInFile("models/_sources.yml", "Identifier of an order.",
				"the source was documented from downstream by default"),
			on: inFile("models/_sources.yml", "Identifier of an order.",
				"the source was not documented from the model below it"),
		},
		{
			key:   "inheritance.backfill.sources_only",
			claim: "limit backfill to source tables",
			build: stageBuildCfg(nil, func(c *config.Config) {
				c.Inheritance.Backfill.Enabled = ptrTo(true)
			}),
			set: func(_ *testing.T, c *config.Config, _ string) {
				c.Inheritance.Backfill.SourcesOnly = ptrTo(false)
			},
			off: notInFile("models/_mid.yml", "Documented downstream",
				"a model was backfilled while backfill was limited to sources"),
			on: inFile("models/_mid.yml", "Documented downstream",
				"the model was not backfilled from the model below it"),
		},

		// --- columns -------------------------------------------------------
		{
			key:   "columns.add_missing",
			claim: "add columns the warehouse has and the YAML does not",
			build: stageBuild(nil),
			set:   func(_ *testing.T, c *config.Config, _ string) { c.Columns.AddMissing = ptrTo(false) },
			off:   has("name: note", "a column in the catalog was not added"),
			on:    lacksIn("name: note", "a column was added with add_missing off"),
		},
		{
			key:   "columns.remove_stale",
			claim: "drop columns the warehouse no longer has",
			build: stageBuild(nil),
			set:   func(_ *testing.T, c *config.Config, _ string) { c.Columns.RemoveStale = ptrTo(false) },
			off:   lacksIn("name: legacy", "a column the catalog does not have was kept"),
			on:    has("name: legacy", "a column was dropped with remove_stale off"),
		},
		{
			key:   "columns.data_types",
			claim: "write each column's data_type from the catalog",
			build: stageBuild(nil),
			set:   func(_ *testing.T, c *config.Config, _ string) { c.Columns.DataTypes = ptrTo(false) },
			off:   has("data_type: INTEGER", "no data type was written"),
			on:    lacksIn("data_type:", "a data type was written with data_types off"),
		},
		{
			key:   "columns.case",
			claim: "the case a newly added column's name is written in",
			build: stageBuild(nil),
			set:   func(_ *testing.T, c *config.Config, _ string) { c.Columns.Case = ptrTo("upper") },
			off:   has("name: note", "the catalog's spelling was not preserved"),
			on: both(has("name: NOTE", "an added column was not upper-cased"),
				has("name: ID", "an already-written spelling was churned")),
		},
		{
			key:   "columns.order",
			claim: "the order columns are written in",
			// The warehouse reports `note` before `amount_cents`, so catalog
			// order and alphabetical order disagree.
			build: stageBuild(func(t *testing.T, s *stage) {
				s.catalog["model.demo.stg"] = catalogNode(
					[3]string{"ID", "INTEGER", ""},
					[3]string{"note", "VARCHAR", ""},
					[3]string{"amount_cents", "BIGINT", ""},
				)
			}),
			set: func(_ *testing.T, c *config.Config, _ string) { c.Columns.Order = ptrTo(config.OrderAlphabetical) },
			off: order("ID", "note", "amount_cents"),
			on:  order("ID", "amount_cents", "note"),
		},
		{
			key:   "columns.expand_structs",
			claim: "document each field of a struct column under its dotted name",
			build: stageBuild(nil),
			set:   func(_ *testing.T, c *config.Config, _ string) { c.Columns.ExpandStructs = ptrTo(false) },
			off:   has("name: profile.first_name", "the struct field was not expanded"),
			on: both(lacksIn("profile.first_name", "the struct was expanded anyway"),
				has("name: profile", "the struct column itself disappeared")),
		},
		{
			key:   "columns.comments",
			claim: "use the warehouse's own column comment as the description",
			build: stageBuild(nil),
			set: func(_ *testing.T, c *config.Config, _ string) {
				c.Columns.Comments = ptrTo(config.WarehouseCommentsNever)
			},
			off: has("Free text, straight from the warehouse.", "the warehouse comment was not used"),
			on: lacksIn("Free text, straight from the warehouse.",
				"the warehouse comment was used with comments: never"),
		},

		// --- organise ------------------------------------------------------
		{
			key:   "organize.enabled",
			claim: "move a model into the file its path rule names",
			build: stageBuild(withPathRule),
			set:   func(_ *testing.T, c *config.Config, _ string) { c.Organize.Enabled = ptrTo(false) },
			off: func(t *testing.T, s snapshot) {
				s.file(t, "models/stg.yml")
				s.missing(t, "models/_stg.yml")
			},
			on: func(t *testing.T, s snapshot) {
				s.file(t, "models/_stg.yml")
				s.missing(t, "models/stg.yml")
			},
		},
		{
			key:   "organize.delete_empty",
			claim: "delete a schema file left with no entries after a move",
			build: stageBuild(withPathRule),
			set:   func(_ *testing.T, c *config.Config, _ string) { c.Organize.DeleteEmpty = ptrTo(false) },
			off: func(t *testing.T, s snapshot) {
				s.missing(t, "models/_stg.yml")
			},
			on: lacksIn("name: stg\n", "the emptied file still lists the model"),
		},

		// --- output --------------------------------------------------------
		{
			key:   "output.comments",
			claim: "what happens to YAML comments inside a column list when columns move",
			// The comment sits above a column that survives the run and is not
			// the first entry, which is exactly the one dbt-osmosis loses.
			build: stageBuild(func(t *testing.T, s *stage) {
				cols := s.node(t, "model.demo.stg")["columns"].(map[string]any)
				cols["amount_cents"] = column("amount_cents", "")
				s.files["models/_stg.yml"] = "version: 2\nmodels:\n  - name: stg\n    columns:\n" +
					"      - name: ID\n" +
					"      # the analyst's note about amounts\n" +
					"      - name: amount_cents\n" +
					"      - name: legacy\n        description: Written by hand, and gone from the warehouse.\n"
			}),
			set: func(_ *testing.T, c *config.Config, _ string) { c.Output.Comments = ptrTo(config.CommentsOsmosis) },
			off: has("# the analyst's note about amounts", "a comment inside the column list was lost"),
			on: lacksIn("# the analyst's note about amounts",
				"dbt-osmosis' comment loss was not reproduced"),
		},
	}
	return append(cases, sourceSettingCases()...)
}

// upstreamManifest writes a bare upstream project — artifacts only, no
// checkout — whose single seed documents `ID` with desc.
func upstreamManifest(t *testing.T, dir, desc string) {
	t.Helper()
	up := newStage()
	up.name = "upstream"
	up.nodes = map[string]any{
		"seed.upstream.raw": map[string]any{
			"unique_id": "seed.upstream.raw", "name": "raw",
			"resource_type": "seed", "package_name": "upstream",
			"columns":    columnMap([]map[string]any{column("ID", desc)}),
			"depends_on": map[string]any{"nodes": []string{}},
		},
	}
	up.sources = map[string]any{}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	up.writeArtifacts(t, dir)
}

// loadConfig writes dbt_ditto.yml into root and loads it, for the settings that
// are read during loading rather than set by a caller.
func loadConfig(t *testing.T, root, body string) *config.Config {
	t.Helper()
	writeFile(t, filepath.Join(root, "dbt_ditto.yml"), body)
	cfg, err := config.Load(filepath.Join(root, "dbt_ditto.yml"))
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

// withDirective gives the staging model's ID column a description that a
// directive marker may or may not claim.
func withDirective(desc string) func(*testing.T, *stage) {
	return func(t *testing.T, s *stage) {
		s.col(t, "model.demo.stg", "ID")["description"] = desc
		s.files["models/_stg.yml"] = strings.Replace(s.files["models/_stg.yml"],
			"      - name: ID\n", "      - name: ID\n        description: \""+desc+"\"\n", 1)
	}
}

// withDerivedColumns adds the columns derived matching is about: an aggregate
// of an upstream column, and a struct field packed from one.
func withDerivedColumns(t *testing.T, s *stage) {
	cols := s.node(t, "model.demo.stg")["columns"].(map[string]any)
	cols["total_amount_cents"] = column("total_amount_cents", "")
	cols["amount_cents_sum"] = column("amount_cents_sum", "")
	s.catalog["model.demo.stg"] = catalogNode(
		[3]string{"ID", "INTEGER", ""},
		[3]string{"total_amount_cents", "BIGINT", ""},
		[3]string{"amount_cents_sum", "BIGINT", ""},
		[3]string{"profile", "STRUCT(first_name VARCHAR)", ""},
	)
}

func enableDerived(c *config.Config) { c.Inheritance.Derived.Enabled = ptrTo(true) }

// withPathRule gives the project a path template, so organising has somewhere
// to move the model to.
func withPathRule(t *testing.T, s *stage) {
	s.projectYAML += "\nmodels:\n  demo:\n    +dbt-ditto-path: \"{model}.yml\"\n"
}

// colBlock returns the YAML of one column entry.
func colBlock(t *testing.T, s snapshot, column string) string {
	t.Helper()
	body := stgFile(t, s)
	start := strings.Index(body, "- name: "+column+"\n")
	if start < 0 {
		t.Fatalf("column %q was not written:\n%s", column, body)
	}
	rest := body[start+1:]
	if end := strings.Index(rest, "\n      - name: "); end >= 0 {
		return rest[:end]
	}
	return rest
}

func hasWarning(s snapshot, substr string) bool {
	for _, w := range s.warnings {
		if strings.Contains(w, substr) {
			return true
		}
	}
	return false
}

// order checks that the named columns appear in this order.
func order(names ...string) check {
	return func(t *testing.T, s snapshot) {
		t.Helper()
		body := stgFile(t, s)
		at := -1
		for _, name := range names {
			i := strings.Index(body, "- name: "+name+"\n")
			if i < 0 {
				t.Fatalf("column %q was not written:\n%s", name, body)
			}
			if i < at {
				t.Errorf("column %q is out of order, wanted %v:\n%s", name, names, body)
				return
			}
			at = i
		}
	}
}

// TestSettingsDoWhatTheyClaim runs every case: the setting has to change the
// run, and change it the way it says it does.
func TestSettingsDoWhatTheyClaim(t *testing.T) {
	for _, c := range settingCases() {
		t.Run(c.key, func(t *testing.T) {
			opts := runner.Options{Organize: true, RefreshSources: c.refresh}

			dirOff, cfgOff := c.build(t)
			off := runStageWith(t, dirOff, cfgOff, opts)

			dirOn, cfgOn := c.build(t)
			c.set(t, cfgOn, dirOn)

			if c.fail {
				// The claim is that the run stops, so there is no second
				// snapshot to compare: not stopping is the failure.
				if _, err := runner.Run(cfgOn, opts); err == nil {
					t.Fatalf("%s changed nothing (claim: %s)", c.key, c.claim)
				}
				c.off(t, off)
				return
			}

			on := runStageWith(t, dirOn, cfgOn, opts)
			if off.equal(on) {
				t.Fatalf("%s changed nothing (claim: %s)", c.key, c.claim)
			}
			c.off(t, off)
			c.on(t, on)
		})
	}
}

// TestEverySettingIsCovered walks the config structs and fails if a key has no case
// above.
func TestEverySettingIsCovered(t *testing.T) {
	covered := map[string]bool{}
	for _, c := range settingCases() {
		if covered[c.key] {
			t.Errorf("two cases for %s", c.key)
		}
		covered[c.key] = true
	}

	for _, key := range configKeys(reflect.TypeOf(config.Config{}), "") {
		if !covered[key] {
			t.Errorf("%s has no case in settingCases(): every setting needs a test proving it does what it claims", key)
		}
		delete(covered, key)
	}
	for key := range covered {
		t.Errorf("%s has a case but is not a config key any more", key)
	}
}

// configKeys returns the dotted yaml paths of every configurable field.
func configKeys(t reflect.Type, prefix string) []string {
	var out []string
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		tag := strings.Split(f.Tag.Get("yaml"), ",")[0]
		if tag == "" || tag == "-" {
			continue
		}
		name := prefix + tag

		ft := f.Type
		for ft.Kind() == reflect.Pointer {
			ft = ft.Elem()
		}
		if ft.Kind() == reflect.Slice {
			elem := ft.Elem()
			for elem.Kind() == reflect.Pointer {
				elem = elem.Elem()
			}
			if elem.Kind() == reflect.Struct {
				out = append(out, configKeys(elem, name+"[].")...)
				continue
			}
		}
		if ft.Kind() == reflect.Struct {
			out = append(out, configKeys(ft, name+".")...)
			continue
		}
		out = append(out, name)
	}
	return out
}
