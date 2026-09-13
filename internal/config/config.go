// Package config loads dbt_ditto.yml.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// ProjectRef points at one dbt project on disk.
type ProjectRef struct {
	// Name is not configurable; dbt decides it. Filled in from a dbt-loom entry.
	Name string `yaml:"-"`
	Path string `yaml:"path"`
	// Target overrides the artifact directory (default <path>/target).
	Target string `yaml:"target"`
	// Manifest points at a manifest.json(.gz) with no project behind it, as a
	// dbt-loom upstream arrives. Always upstream: no YAML on disk.
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
	// Progenitor records the node a description came from in the column's meta.
	Progenitor    *bool   `yaml:"progenitor"`
	ProgenitorKey *string `yaml:"progenitor_key"`
	// SkipMetaKeys are meta keys never inherited (e.g. ownership).
	SkipMetaKeys []string `yaml:"skip_meta_keys"`
	// ExtraKeys are unmodelled column keys to carry down the DAG (`policy_tags`).
	ExtraKeys []string `yaml:"extra_keys"`
	// Directives honours `description: "Inherited: model.column"` as a pointer.
	Directives      *bool   `yaml:"directives"`
	DirectivePrefix *string `yaml:"directive_prefix"`
	// WarnAmbiguous reports a column several parents document differently, where
	// unique_id order picks the winner.
	WarnAmbiguous *bool `yaml:"warn_ambiguous"`
	// AmbiguityMeta records that disagreement in the column's meta, so it outlives
	// the warning. Only an inherited description is annotated. Off for parity.
	AmbiguityMeta *bool   `yaml:"ambiguity_meta"`
	AmbiguityKey  *string `yaml:"ambiguity_key"`
	// Derived matches columns whose name changed on the way down the DAG.
	Derived Derived `yaml:"derived"`
	// Backfill takes documentation from downstream when there is none upstream.
	Backfill Backfill `yaml:"backfill"`
}

// Backfill carries documentation *up* the DAG from a node's descendants, the
// only way to document a source. Strictly additive: it only fills blanks.
type Backfill struct {
	Enabled *bool `yaml:"enabled"`
	// SourcesOnly limits backfill to source tables.
	SourcesOnly *bool `yaml:"sources_only"`
}

// Derived matches a column to an upstream one it no longer shares a name with:
// aggregated, or packed into / unpacked out of a struct. Off, for parity.
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
	// ExpandStructs writes an entry per struct field, under dbt's dotted path.
	ExpandStructs *bool `yaml:"expand_structs"`
	// Comments uses the warehouse column comment as the description: "new"
	// (dbt-osmosis' behaviour), "always" or "never".
	Comments *string `yaml:"comments"`
}

// Organize controls YAML file placement.
type Organize struct {
	Enabled *bool `yaml:"enabled"`
	// DeleteEmpty removes schema files left with no entries after a move.
	DeleteEmpty *bool `yaml:"delete_empty"`
}

// Output controls the shape of what is written.
type Output struct {
	// Comments: "follow" keeps each YAML comment with the column it annotates;
	// "osmosis" keeps only the comment above the first entry, as dbt-osmosis does.
	Comments *string `yaml:"comments"`
}

// Sources documents external sources — the raw tables no loaded dbt project builds — by
// asking a separate program, so the binary itself still connects to nothing.
type Sources struct {
	Providers []SourceProvider `yaml:"providers"`
	// Strict fails the run when a provider does.
	Strict *bool `yaml:"strict"`
	// Cache, relative to the config file, is what keeps `--check` offline.
	Cache  *string `yaml:"cache"`
	Labels Labels  `yaml:"labels"`
}

// SourceProvider is one external program and the sources it answers for.
type SourceProvider struct {
	// Command is run through the platform shell, from the config file's directory.
	Command string `yaml:"command"`
	// Match limits which sources this provider is asked about.
	Match SourceMatch `yaml:"match"`
}

// SourceMatch selects sources by where they live, with `*` globs.
type SourceMatch struct {
	Database string `yaml:"database"`
	Schema   string `yaml:"schema"`
}

// Labels routes the key-value metadata a provider reports (BigQuery labels,
// Snowflake tags, Glue table parameters) into dbt's vocabulary, here rather
// than per provider.
type Labels struct {
	// Mode: "meta" (default), "tags", "both" or "ignore".
	Mode *string `yaml:"mode"`
	// MetaKey nests the pairs under one meta key, keeping them distinguishable from
	// hand-written meta, which the propagation rules depend on.
	MetaKey *string `yaml:"meta_key"`
	// TagFormat renders a pair as a tag. An empty value renders as the bare key.
	TagFormat *string `yaml:"tag_format"`
	// Include, when non-empty, is the only keys accepted; Exclude then drops from
	// what remains. Both match with `*` globs.
	Include []string `yaml:"include"`
	Exclude []string `yaml:"exclude"`

	Propagate LabelPropagate `yaml:"propagate"`
}

// LabelPropagate decides which labels travel down the DAG with the column.
type LabelPropagate struct {
	// Column labels describe the data, so they travel like a description.
	Column *bool `yaml:"column"`
	// Structs carries labels across a struct pack/unpack: the same bytes, reshaped.
	Structs *bool `yaml:"structs"`
	// Aggregates: "inherit", "warn" (default) or "ignore".
	Aggregates *string `yaml:"aggregates"`
	// OnConflict when one generation disagrees on a value: "warn" (default, writing
	// nothing), "first" (lowest unique_id, as descriptions do) or "none".
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

	// Loom reads dbt_loom.config.yml, on so the upstream list is not kept twice.
	Loom *bool `yaml:"loom"`

	Dir string `yaml:"-"`

	// Notes are non-fatal remarks made while loading, printed once before the run.
	Notes []string `yaml:"-"`
}

// Filenames searched when no config path is given, in order within a directory.
var Filenames = []string{"dbt_ditto.yml", "dbt_ditto.yaml", ".dbt_ditto.yml", PyprojectFilename}

// Load reads the config at path.
func Load(path string) (*Config, error) {
	if path == "" {
		dir, err := os.Getwd()
		if err != nil {
			return nil, err
		}
		if path, err = discoverFrom(dir); err != nil {
			return nil, err
		}
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

// SourceCachePath resolves the cache against the config file's own directory.
func (c *Config) SourceCachePath() string {
	return AbsTo(c.Dir, strOr(c.Sources.Cache, DefaultSourceCache))
}

// AbsTo resolves a configured path against the directory it was configured in.
func AbsTo(dir, path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(dir, path)
}

// DefaultSourceCache sits under `target/`, which projects already gitignore.
const DefaultSourceCache = "target/ditto-sources.json"

// Default builds a single-project config for `dbt-ditto inherit <dir>`.
func Default(projectDir string) *Config {
	abs, _ := filepath.Abs(projectDir)
	c := &Config{
		Dir:      abs,
		Projects: []ProjectRef{{Path: abs}},
	}
	c.attachLoomUpstreams()
	return c
}

// discoverFrom walks upwards from dir looking for a config this tool owns.
func discoverFrom(dir string) (string, error) {
	for {
		for _, name := range Filenames {
			p := filepath.Join(dir, name)
			if _, err := os.Stat(p); err != nil {
				continue
			}
			// A pyproject.toml without the table is not an answer; keep going up.
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

// DefaultAmbiguityKey is namespaced: dbt-osmosis has no equivalent.
const DefaultAmbiguityKey = "dbt_ditto_ambiguous"

// DefaultDirectivePrefix is dbt-doc-inherit's marker, so those projects work.
const DefaultDirectivePrefix = "Inherited:"

// Resolved is the config with every default filled in, so nothing downstream
// deals with nil pointers.
type Resolved struct {
	InheritColumns, InheritNodeDescription, InheritMeta, InheritTags bool
	CaseInsensitive, Force                                           bool
	Progenitor, Directives, WarnAmbiguous, AmbiguityMeta             bool
	ProgenitorKey, DirectivePrefix, AmbiguityKey                     string
	Placeholders, SkipMetaKeys                                       map[string]bool

	AddMissing, RemoveStale, DataTypes bool
	ColumnCase, ColumnOrder            string

	ExpandStructs     bool
	WarehouseComments string

	Backfill, BackfillSourcesOnly bool

	DerivedStructs, DerivedAggregates bool
	DerivedPrefixes, DerivedSuffixes  []string

	Organize, DeleteEmpty bool

	Comments  string
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

// DefaultLabelMetaKey namespaces provider labels inside meta, which is what
// keeps them identifiable to the propagation rules.
const DefaultLabelMetaKey = "labels"

// DefaultTagFormat renders a label pair as a dbt tag.
const DefaultTagFormat = "{key}:{value}"

// ResolvedLabels is Labels with the defaults filled in.
type ResolvedLabels struct {
	Mode, MetaKey, TagFormat string
	Include, Exclude         []string

	PropagateColumn, PropagateStructs bool
	Aggregates, OnConflict            string
}

// Routed reports whether labels end up anywhere at all.
func (l ResolvedLabels) Routed() bool { return l.Mode != LabelsIgnore }

// ToMeta reports whether labels are written into meta.
func (l ResolvedLabels) ToMeta() bool { return l.Mode == LabelsMeta || l.Mode == LabelsBoth }

// ToTags reports whether labels are written as tags.
func (l ResolvedLabels) ToTags() bool { return l.Mode == LabelsTags || l.Mode == LabelsBoth }

// Nested reports whether labels are kept under their own meta key.
func (l ResolvedLabels) Nested() bool { return l.ToMeta() && l.MetaKey != "" }

// DefaultPlaceholders is dbt-osmosis' list verbatim: an upstream description equal to
// one of these is not inherited.
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
		// On, unlike dbt-osmosis: an inherited description is the one line in a
		// schema file nobody wrote, so its origin is worth recording.
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

	placeholders := sliceOr(c.Inheritance.Placeholders, DefaultPlaceholders)
	// The capacity is a hint, so the empty string below is not counted: the
	// arithmetic to include it is what go/allocation-size-overflow objects to,
	// and a map that grows by one entry costs nothing worth the suppression.
	r.Placeholders = make(map[string]bool, len(placeholders))
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

	// The master switch gates both strategies, so `enabled: true` suffices.
	if boolOr(c.Inheritance.Derived.Enabled, false) {
		r.DerivedStructs = boolOr(c.Inheritance.Derived.Structs, true)
		r.DerivedAggregates = boolOr(c.Inheritance.Derived.Aggregates, true)
		r.DerivedPrefixes = sliceOr(c.Inheritance.Derived.Prefixes, DefaultAggregatePrefixes)
		r.DerivedSuffixes = sliceOr(c.Inheritance.Derived.Suffixes, DefaultAggregateSuffixes)
	}
	return r
}

// Words analysts put around an aggregated column name, so `total_amount_cents`
// matches `amount_cents`. Only whole underscore-separated words are stripped.
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

// IsPlaceholder reports whether a description counts as undocumented, and so
// may be overwritten by an inherited one.
func (r Resolved) IsPlaceholder(desc string) bool { return r.Placeholders[desc] }

// Fold normalises a column name for comparison, honouring case_insensitive, so
// every lookup keys columns the way the configuration says they match.
func (r Resolved) Fold(s string) string {
	if r.CaseInsensitive {
		return strings.ToLower(s)
	}
	return s
}

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

// sliceOr keeps the configured list, defaulting only when the key is absent: an
// empty list is a choice, a missing one is not.
func sliceOr(v, def []string) []string {
	if v == nil {
		return def
	}
	return v
}
