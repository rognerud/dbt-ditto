// Package config loads dbt_ditto.yml. Every default matches dbt-osmosis, with
// one exception: inheritance.progenitor is on, so byte parity needs
// `progenitor: false` (which scripts/parity.sh sets).
//
// docs/usage.md documents every key.
package config

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// ProjectRef points at one dbt project on disk.
type ProjectRef struct {
	// Name is not configurable; dbt decides it. Filled in from a dbt-loom entry,
	// which names a manifest before anything has read it.
	Name string `yaml:"-"`
	Path string `yaml:"path"`
	// Target overrides the artifact directory (default <path>/target).
	Target string `yaml:"target"`
	// Manifest points straight at a manifest.json(.gz) with no project directory
	// behind it, as a dbt-loom upstream arrives. Always upstream: no YAML on disk.
	Manifest string `yaml:"manifest"`
	// Upstream projects donate metadata but are never written to.
	Upstream bool `yaml:"upstream"`
}

// Inheritance controls what gets copied down the DAG.
type Inheritance struct {
	Columns         *bool    `yaml:"columns"`
	NodeDescription *bool    `yaml:"node_description"`
	Meta            *bool    `yaml:"meta"`
	Tags            *bool    `yaml:"tags"`
	CaseInsensitive *bool    `yaml:"case_insensitive"`
	Force           *bool    `yaml:"force"`
	Placeholders    []string `yaml:"placeholders"`
	// Progenitor records the node a description came from under the column's
	// meta, as dbt-osmosis' --add-progenitor-to-meta does.
	Progenitor    *bool   `yaml:"progenitor"`
	ProgenitorKey *string `yaml:"progenitor_key"`
	// SkipMetaKeys are meta keys never inherited (e.g. ownership).
	SkipMetaKeys []string `yaml:"skip_meta_keys"`
	// ExtraKeys are column keys dbt-ditto does not otherwise model but should
	// carry down the DAG, `policy_tags` being the motivating case. dbt-osmosis
	// calls it `add-inheritance-for-specified-keys`. Explicit rather than a
	// catch-all: decoding every unknown key allocates a map per column.
	ExtraKeys []string `yaml:"extra_keys"`
	// Directives honours `description: "Inherited: stg_customers.customer_id"`
	// as a pointer, for columns renamed on the way down the DAG.
	Directives      *bool   `yaml:"directives"`
	DirectivePrefix *string `yaml:"directive_prefix"`
	// WarnAmbiguous reports a column that several parents document differently,
	// where the winner is decided by unique_id order and is therefore arbitrary.
	WarnAmbiguous *bool `yaml:"warn_ambiguous"`
	// AmbiguityMeta records that disagreement in the column's meta, so it
	// outlives the warning. Only an actually-inherited description is annotated,
	// so settling the disagreement removes it. Off: dbt-osmosis writes no such meta.
	AmbiguityMeta *bool   `yaml:"ambiguity_meta"`
	AmbiguityKey  *string `yaml:"ambiguity_key"`
	// Derived matches columns whose name changed on the way down the DAG.
	Derived Derived `yaml:"derived"`
	// Backfill takes documentation from downstream when there is none upstream.
	Backfill Backfill `yaml:"backfill"`
}

// Backfill carries documentation *up* the DAG, from a node's descendants, which
// is the only way to document a source: a source is a root of the DAG. Strictly
// additive — it only fills blanks.
type Backfill struct {
	Enabled *bool `yaml:"enabled"`
	// SourcesOnly limits backfill to source tables.
	SourcesOnly *bool `yaml:"sources_only"`
}

// Derived matches a column to an upstream column it no longer shares a name
// with: aggregated, or packed into / unpacked out of a struct. Off by default —
// dbt-osmosis matches on name alone, and parity is the promise.
type Derived struct {
	Enabled *bool `yaml:"enabled"`
	// Structs matches a struct field to the flat column it was packed from, and
	// the reverse, by the last segment of the dotted path.
	Structs *bool `yaml:"structs"`
	// Aggregates matches by stripping a leading or trailing aggregate word.
	Aggregates *bool `yaml:"aggregates"`
	// Prefixes and Suffixes replace the built-in aggregate word lists.
	Prefixes []string `yaml:"prefixes"`
	Suffixes []string `yaml:"suffixes"`
}

// Columns controls reconciliation against the warehouse catalog.
type Columns struct {
	AddMissing  *bool   `yaml:"add_missing"`
	RemoveStale *bool   `yaml:"remove_stale"`
	DataTypes   *bool   `yaml:"data_types"`
	Case        *string `yaml:"case"`  // lower | upper | preserve
	Order       *string `yaml:"order"` // catalog | yaml | alphabetical
	// ExpandStructs writes an entry per struct field, with dbt's dotted path
	// (`profile.first_name`). Not an extension: adapters that understand nested
	// data report those as columns, but catalog.json holds one composite type,
	// so the type has to be expanded to reach the same answer.
	ExpandStructs *bool `yaml:"expand_structs"`
	// Comments uses the warehouse column comment as the description: "new"
	// (dbt-osmosis' behaviour), "always" or "never". Often a source table's only
	// documentation, since nothing upstream can reach it.
	Comments *string `yaml:"comments"`
}

// Organize controls YAML file placement.
type Organize struct {
	Enabled *bool `yaml:"enabled"`
	// DeleteEmpty removes schema files left with no entries after a move.
	DeleteEmpty *bool `yaml:"delete_empty"`
}

// Output controls the shape of what is written. Whether column meta and tags
// nest under `config:` is not a setting: the manifest's dbt version decides it
// (>= 1.9.6 reads them there, older dbt does not read them there at all).
type Output struct {
	// Comments: "follow" keeps each YAML comment with the column it annotates;
	// "osmosis" reproduces dbt-osmosis, which rebuilds the column list and so
	// keeps only the comment above the first entry.
	Comments *string `yaml:"comments"`
}

// Sources documents external sources — the raw tables no loaded dbt project
// builds — by asking an external program. A provider is a separate program so
// the binary keeps its premise of reading artifacts off disk and connecting to
// nothing. See docs/source-providers.md.
type Sources struct {
	Providers []SourceProvider `yaml:"providers"`
	// Strict fails the run when a provider does. Off: a tool that tidies YAML
	// should still tidy it when BigQuery is unreachable.
	Strict *bool `yaml:"strict"`
	// Cache, relative to the config file, is what keeps `--check` offline: CI
	// reads a committed cache and spawns nothing, so it needs no credentials.
	Cache  *string `yaml:"cache"`
	Labels Labels  `yaml:"labels"`
}

// SourceProvider is one external program and the sources it answers for.
type SourceProvider struct {
	// Command is run through the platform shell, from the config file's directory.
	Command string `yaml:"command"`
	// Match limits which sources this provider is asked about. Empty patterns
	// match everything; the first provider to claim a source wins.
	Match SourceMatch `yaml:"match"`
}

// SourceMatch selects sources by where they live, with `*` globs.
type SourceMatch struct {
	Database string `yaml:"database"`
	Schema   string `yaml:"schema"`
}

// Labels routes the key-value metadata a provider reports (BigQuery labels,
// Snowflake tags, Glue table parameters) into dbt's vocabulary. Mapped here
// rather than per provider, so providers do not each invent a config for it.
type Labels struct {
	// Mode: "meta" (default), "tags", "both" or "ignore". Meta, because a dbt
	// tag is a selector: routing labels there changes what `--select tag:...`
	// matches in a project that never asked for it.
	Mode *string `yaml:"mode"`
	// MetaKey nests the pairs under one meta key, which is what keeps them
	// distinguishable from hand-written meta and so what the propagation rules
	// depend on. Empty flattens them into meta, at the cost of those rules.
	MetaKey *string `yaml:"meta_key"`
	// TagFormat renders a pair as a tag. A pair with an empty value renders as
	// the bare key regardless: BigQuery permits valueless labels.
	TagFormat *string `yaml:"tag_format"`
	// Include, when non-empty, is the only label keys accepted. Exclude then
	// drops from what remains. Both match with `*` globs.
	Include []string `yaml:"include"`
	Exclude []string `yaml:"exclude"`

	Propagate LabelPropagate `yaml:"propagate"`
}

// LabelPropagate decides which labels travel down the DAG with the column.
// There is deliberately no `table` key: a relation's own labels describe the
// physical object, and node meta is not inherited by anything here.
type LabelPropagate struct {
	// Column labels describe the data, so they travel like a description. Off
	// relies on the labels being nested under MetaKey, which is what makes them
	// identifiable; labels routed to tags travel regardless, as dbt tags do.
	Column *bool `yaml:"column"`
	// Structs carries labels across a struct pack/unpack match: the field holds
	// the same bytes the flat column did.
	Structs *bool `yaml:"structs"`
	// Aggregates: "inherit", "warn" (default) or "ignore". Warn, because a
	// missing classification reads as "not restricted", which is a false claim,
	// while the description itself still applies.
	Aggregates *string `yaml:"aggregates"`
	// OnConflict when one generation disagrees on a value: "warn" (default,
	// writing nothing), "first" (lowest unique_id, as descriptions do) or
	// "none". Descriptions break the tie arbitrarily; a classification cannot,
	// since ranking restrictiveness needs an ordering this tool cannot learn.
	OnConflict *string `yaml:"on_conflict"`
}

// Config is the whole dbt_ditto.yml.
type Config struct {
	Projects    []ProjectRef `yaml:"projects"`
	Inheritance Inheritance  `yaml:"inheritance"`
	Columns     Columns      `yaml:"columns"`
	Organize    Organize     `yaml:"organize"`
	Output      Output       `yaml:"output"`
	Sources     Sources      `yaml:"sources"`

	// Loom turns off dbt_loom.config.yml discovery, on by default so the
	// upstream manifest list is not maintained twice.
	Loom *bool `yaml:"loom"`

	Dir string `yaml:"-"`

	// Notes are non-fatal remarks made while loading, such as a dbt-loom
	// manifest this tool cannot reach. They are printed once, before the run.
	Notes []string `yaml:"-"`
}

// Filenames searched when no explicit config path is given, in order within
// each directory. PyprojectFilename last, so a directory holding both uses the
// dedicated file.
var Filenames = []string{"dbt_ditto.yml", "dbt_ditto.yaml", ".dbt_ditto.yml", PyprojectFilename}

// Load reads the config at path. If path is empty it searches the working
// directory and its parents.
func Load(path string) (*Config, error) {
	if path == "" {
		found, err := discover()
		if err != nil {
			return nil, err
		}
		path = found
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	c := &Config{}
	if isPyproject(path) {
		if err := unmarshalPyproject(raw, c); err != nil {
			return nil, fmt.Errorf("parse %s: %w", path, err)
		}
	} else if err := yaml.Unmarshal(raw, c); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	c.Dir = filepath.Dir(abs)
	if len(c.Projects) == 0 {
		return nil, fmt.Errorf("%s: no projects configured", path)
	}
	c.attachLoomUpstreams()
	return c, nil
}

// SourceCachePath resolves the cache against the config file's own directory,
// so a run from anywhere reads the same file.
func (c *Config) SourceCachePath() string {
	p := DefaultSourceCache
	if c.Sources.Cache != nil && *c.Sources.Cache != "" {
		p = *c.Sources.Cache
	}
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(c.Dir, p)
}

// DefaultSourceCache sits under `target/`, which projects already gitignore.
const DefaultSourceCache = "target/ditto-sources.json"

// Default builds a single-project config for `dbt-ditto inherit <dir>` with no
// config file present.
func Default(projectDir string) *Config {
	abs, _ := filepath.Abs(projectDir)
	c := &Config{
		Dir:      abs,
		Projects: []ProjectRef{{Path: abs}},
	}
	c.attachLoomUpstreams()
	return c
}

func discover() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	return discoverFrom(dir)
}

// discoverFrom walks upwards from dir looking for a config this tool owns.
func discoverFrom(dir string) (string, error) {
	for {
		for _, name := range Filenames {
			p := filepath.Join(dir, name)
			if _, err := os.Stat(p); err != nil {
				continue
			}
			// A pyproject.toml is only an answer if it carries the table;
			// otherwise carry on upwards as if it were not there.
			if isPyproject(p) && !hasPyprojectTable(p) {
				continue
			}
			return p, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("no dbt_ditto.yml, and no [%s] table in a %s, found (searched upwards from the working directory)",
				PyprojectTable, PyprojectFilename)
		}
		dir = parent
	}
}

// Column ordering modes.
const (
	OrderCatalog      = "catalog"
	OrderYAML         = "yaml"
	OrderAlphabetical = "alphabetical"
)

// Handling of comments already in the YAML file.
const (
	CommentsFollow  = "follow"
	CommentsOsmosis = "osmosis"
)

// Use of the warehouse's own column comments as descriptions.
const (
	WarehouseCommentsNew    = "new"
	WarehouseCommentsAlways = "always"
	WarehouseCommentsNever  = "never"
)

// DefaultProgenitorKey matches the meta key dbt-osmosis writes.
const DefaultProgenitorKey = "osmosis_progenitor"

// DefaultAmbiguityKey is namespaced to this tool: dbt-osmosis has no equivalent.
const DefaultAmbiguityKey = "dbt_ditto_ambiguous"

// DefaultDirectivePrefix is the marker dbt-doc-inherit uses, so a project
// already writing these keeps working.
const DefaultDirectivePrefix = "Inherited:"

// Resolved is the config with every default filled in, so downstream code never
// deals with nil pointers.
type Resolved struct {
	InheritColumns         bool
	InheritNodeDescription bool
	InheritMeta            bool
	InheritTags            bool
	CaseInsensitive        bool
	Force                  bool
	Placeholders           map[string]bool
	Progenitor             bool
	ProgenitorKey          string
	SkipMetaKeys           map[string]bool
	Directives             bool
	DirectivePrefix        string
	WarnAmbiguous          bool
	AmbiguityMeta          bool
	AmbiguityKey           string

	AddMissing  bool
	RemoveStale bool
	DataTypes   bool
	ColumnCase  string
	ColumnOrder string

	ExpandStructs     bool
	WarehouseComments string

	Backfill            bool
	BackfillSourcesOnly bool

	DerivedStructs    bool
	DerivedAggregates bool
	DerivedPrefixes   []string
	DerivedSuffixes   []string

	Organize    bool
	DeleteEmpty bool

	Comments string

	ExtraKeys []string

	SourcesStrict bool
	SourceLabels  ResolvedLabels
}

// Label routing modes.
const (
	LabelsMeta   = "meta"
	LabelsTags   = "tags"
	LabelsBoth   = "both"
	LabelsIgnore = "ignore"
)

// What happens to a label at an aggregate match.
const (
	AggregatesInherit = "inherit"
	AggregatesWarn    = "warn"
	AggregatesIgnore  = "ignore"
)

// What happens when one generation disagrees about a label's value.
const (
	ConflictWarn  = "warn"
	ConflictFirst = "first"
	ConflictNone  = "none"
)

// DefaultLabelMetaKey namespaces provider-reported labels inside meta, which is
// what keeps them identifiable for the propagation rules.
const DefaultLabelMetaKey = "labels"

// DefaultTagFormat renders a label pair as a dbt tag.
const DefaultTagFormat = "{key}:{value}"

// ResolvedLabels is Labels with the defaults filled in.
type ResolvedLabels struct {
	Mode      string
	MetaKey   string
	TagFormat string
	Include   []string
	Exclude   []string

	PropagateColumn  bool
	PropagateStructs bool
	Aggregates       string
	OnConflict       string
}

// Routed reports whether labels end up anywhere at all.
func (l ResolvedLabels) Routed() bool { return l.Mode != LabelsIgnore }

// ToMeta reports whether labels are written into meta.
func (l ResolvedLabels) ToMeta() bool { return l.Mode == LabelsMeta || l.Mode == LabelsBoth }

// ToTags reports whether labels are written as tags.
func (l ResolvedLabels) ToTags() bool { return l.Mode == LabelsTags || l.Mode == LabelsBoth }

// Nested reports whether labels are kept under their own meta key, which the
// propagation rules depend on.
func (l ResolvedLabels) Nested() bool { return l.ToMeta() && l.MetaKey != "" }

// DefaultPlaceholders is dbt-osmosis' placeholder list verbatim: an upstream
// description equal to one of these is not inherited. Comparison is exact — no
// trimming, no case folding — because dbt-osmosis uses a plain `in`.
// The empty string is always a placeholder.
var DefaultPlaceholders = []string{
	"",
	"Pending further documentation",
	"No description for this column",
	"Not documented",
	"Undefined",
}

// Resolve applies defaults to the parsed config.
func (c *Config) Resolve() Resolved {
	r := Resolved{
		InheritColumns: boolOr(c.Inheritance.Columns, true),
		// dbt-osmosis inherits column knowledge only, so this is opt-in.
		InheritNodeDescription: boolOr(c.Inheritance.NodeDescription, false),
		InheritMeta:            boolOr(c.Inheritance.Meta, true),
		InheritTags:            boolOr(c.Inheritance.Tags, true),
		CaseInsensitive:        boolOr(c.Inheritance.CaseInsensitive, true),
		Force:                  boolOr(c.Inheritance.Force, false),
		// On by default, unlike dbt-osmosis: an inherited description is the one
		// thing in a schema file nobody wrote, so its origin is worth recording.
		Progenitor:        boolOr(c.Inheritance.Progenitor, true),
		ProgenitorKey:     strOr(c.Inheritance.ProgenitorKey, DefaultProgenitorKey),
		Directives:        boolOr(c.Inheritance.Directives, true),
		DirectivePrefix:   strOr(c.Inheritance.DirectivePrefix, DefaultDirectivePrefix),
		WarnAmbiguous:     boolOr(c.Inheritance.WarnAmbiguous, true),
		AmbiguityMeta:     boolOr(c.Inheritance.AmbiguityMeta, false),
		AmbiguityKey:      strOr(c.Inheritance.AmbiguityKey, DefaultAmbiguityKey),
		AddMissing:        boolOr(c.Columns.AddMissing, true),
		RemoveStale:       boolOr(c.Columns.RemoveStale, true),
		DataTypes:         boolOr(c.Columns.DataTypes, true),
		ColumnCase:        strOr(c.Columns.Case, "preserve"),
		ColumnOrder:       strOr(c.Columns.Order, OrderCatalog),
		ExpandStructs:     boolOr(c.Columns.ExpandStructs, true),
		WarehouseComments: strOr(c.Columns.Comments, WarehouseCommentsNew),
		Organize:          boolOr(c.Organize.Enabled, true),
		DeleteEmpty:       boolOr(c.Organize.DeleteEmpty, true),
		Comments:          strOr(c.Output.Comments, CommentsFollow),
	}

	placeholders := c.Inheritance.Placeholders
	if placeholders == nil {
		placeholders = DefaultPlaceholders
	}
	r.Placeholders = make(map[string]bool, len(placeholders)+1)
	r.Placeholders[""] = true
	for _, p := range placeholders {
		r.Placeholders[p] = true
	}

	r.SkipMetaKeys = make(map[string]bool, len(c.Inheritance.SkipMetaKeys))
	for _, k := range c.Inheritance.SkipMetaKeys {
		r.SkipMetaKeys[k] = true
	}
	r.ExtraKeys = c.Inheritance.ExtraKeys

	r.SourcesStrict = boolOr(c.Sources.Strict, false)
	l := c.Sources.Labels
	r.SourceLabels = ResolvedLabels{
		Mode:      strOr(l.Mode, LabelsMeta),
		TagFormat: strOr(l.TagFormat, DefaultTagFormat),
		Include:   l.Include,
		Exclude:   l.Exclude,

		PropagateColumn:  boolOr(l.Propagate.Column, true),
		PropagateStructs: boolOr(l.Propagate.Structs, true),
		Aggregates:       strOr(l.Propagate.Aggregates, AggregatesWarn),
		OnConflict:       strOr(l.Propagate.OnConflict, ConflictWarn),
	}
	// Not strOr: an empty meta_key is the documented way to flatten labels.
	r.SourceLabels.MetaKey = DefaultLabelMetaKey
	if l.MetaKey != nil {
		r.SourceLabels.MetaKey = *l.MetaKey
	}
	// Reuse the skip-list mechanism rather than adding a second one.
	if !r.SourceLabels.PropagateColumn && r.SourceLabels.Nested() {
		r.SkipMetaKeys[r.SourceLabels.MetaKey] = true
	}

	r.Backfill = boolOr(c.Inheritance.Backfill.Enabled, false)
	r.BackfillSourcesOnly = boolOr(c.Inheritance.Backfill.SourcesOnly, true)

	// The master switch gates both strategies, so `derived: {enabled: true}` is
	// enough on its own.
	if boolOr(c.Inheritance.Derived.Enabled, false) {
		r.DerivedStructs = boolOr(c.Inheritance.Derived.Structs, true)
		r.DerivedAggregates = boolOr(c.Inheritance.Derived.Aggregates, true)
		r.DerivedPrefixes = c.Inheritance.Derived.Prefixes
		if r.DerivedPrefixes == nil {
			r.DerivedPrefixes = DefaultAggregatePrefixes
		}
		r.DerivedSuffixes = c.Inheritance.Derived.Suffixes
		if r.DerivedSuffixes == nil {
			r.DerivedSuffixes = DefaultAggregateSuffixes
		}
	}
	return r
}

// Words analysts put around an aggregated column name, so `total_amount_cents`
// matches `amount_cents`. Only whole underscore-separated words are stripped,
// so `count_of_things` stays whole.
var (
	DefaultAggregatePrefixes = []string{
		"sum", "total", "avg", "average", "mean", "median", "min", "max",
		"count", "num", "number_of", "n", "first", "last", "cumulative",
		"running", "distinct",
	}
	DefaultAggregateSuffixes = []string{
		"sum", "total", "avg", "average", "mean", "median", "min", "max",
		"count", "cnt", "n",
	}
)

// IsPlaceholder reports whether a description counts as undocumented and may
// therefore be overwritten by an inherited one.
func (r Resolved) IsPlaceholder(desc string) bool { return r.Placeholders[desc] }

func boolOr(p *bool, def bool) bool {
	if p == nil {
		return def
	}
	return *p
}

func strOr(p *string, def string) string {
	if p == nil || *p == "" {
		return def
	}
	return *p
}
