// Package config loads dbt_ditto.yml, the single file an analyst edits to say
// which projects take part and how aggressive inheritance should be.
//
// Every default here is chosen to match dbt-osmosis' out-of-the-box behaviour,
// so pointing dbt-ditto at a project that dbt-osmosis already manages produces
// the same YAML. Where a knob exists that dbt-osmosis does not have, its default
// is the dbt-osmosis behaviour.
//
// There is one deliberate exception: `inheritance.progenitor` is on, so every
// inherited description records where it came from. Byte parity with
// dbt-osmosis therefore needs `progenitor: false`, which is what
// scripts/parity.sh sets.
package config

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// ProjectRef points at one dbt project on disk.
type ProjectRef struct {
	// Name is not configurable: a project's name is whatever dbt_project.yml or
	// the manifest says it is, and that name decides which nodes belong to the
	// project. Letting it be overridden here could only either agree with dbt —
	// achieving nothing — or disagree, and silently select no nodes at all.
	// It is filled in from a dbt-loom entry, which names a manifest before
	// anything has read it.
	Name string `yaml:"-"`
	Path string `yaml:"path"`
	// Target overrides the artifact directory (default <path>/target).
	Target string `yaml:"target"`
	// Manifest points straight at a manifest.json (or manifest.json.gz) with no
	// project directory behind it. This is how a dbt-loom upstream arrives: loom
	// hands dbt a manifest, not a checkout. A manifest-only project is always
	// upstream, because there is no YAML on disk here to write back to.
	Manifest string `yaml:"manifest"`
	// Upstream projects donate metadata but are never written to. This is the
	// dbt-loom case: you have the producer project's manifest but not the right
	// to edit its YAML from here.
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
	Progenitor *bool `yaml:"progenitor"`
	// ProgenitorKey is the meta key used for that annotation.
	ProgenitorKey *string `yaml:"progenitor_key"`
	// SkipMetaKeys are meta keys never inherited (e.g. ownership).
	SkipMetaKeys []string `yaml:"skip_meta_keys"`
	// ExtraKeys are column keys dbt-ditto does not otherwise model but should
	// carry down the DAG, `policy_tags` being the case that motivates it: a
	// classification set on a staging column has every reason to reach the
	// marts, and no reason for this tool to understand what it means.
	//
	// dbt-osmosis calls the same feature `add-inheritance-for-specified-keys`.
	// The list is explicit rather than a catch-all because decoding every
	// unknown key allocates a map per column on a manifest that may hold
	// millions of them.
	ExtraKeys []string `yaml:"extra_keys"`
	// Directives honours an explicit pointer written in place of a description:
	//
	//   description: "Inherited: stg_customers.customer_id"
	//
	// Name matching cannot follow a column that was renamed on the way down the
	// DAG, and no amount of guessing should be trusted to. The directive is the
	// analyst saying where the documentation actually lives.
	Directives *bool `yaml:"directives"`
	// DirectivePrefix is the marker that turns a description into a pointer.
	DirectivePrefix *string `yaml:"directive_prefix"`
	// WarnAmbiguous reports a column that several parents document differently,
	// where the winner is decided by unique_id order and is therefore arbitrary.
	WarnAmbiguous *bool `yaml:"warn_ambiguous"`
	// AmbiguityMeta records that disagreement in the column's meta, the way
	// Progenitor records where the description came from. A warning on stderr is
	// gone the moment the terminal scrolls; the annotation stays next to the
	// description it qualifies, which is where someone reading the file later
	// needs to see that the wording was picked arbitrarily.
	//
	// Only a description that was actually inherited is annotated: settling the
	// disagreement locally, or with a directive, removes the annotation on the
	// next run. Off by default, because it writes meta dbt-osmosis would not.
	AmbiguityMeta *bool `yaml:"ambiguity_meta"`
	// AmbiguityKey is the meta key used for that annotation.
	AmbiguityKey *string `yaml:"ambiguity_key"`
	// Derived matches columns whose name changed on the way down the DAG.
	Derived Derived `yaml:"derived"`
	// Backfill takes documentation from downstream when there is none upstream.
	Backfill Backfill `yaml:"backfill"`
}

// Backfill carries documentation *up* the DAG, from a node's descendants.
//
// Inheritance normally runs downhill, which leaves source tables out: a source
// is a root, so nothing upstream can ever document it. In practice the
// documentation does exist, one step downstream, in the staging model that
// reads the source. Backfill puts it where it belongs.
//
// It is strictly additive. A column that already has a description keeps it, so
// this can never overwrite something written by hand; it only fills blanks.
type Backfill struct {
	Enabled *bool `yaml:"enabled"`
	// SourcesOnly limits backfill to source tables, which is the case that
	// motivates it. Turning it off backfills any undocumented column from
	// whatever documents it downstream.
	SourcesOnly *bool `yaml:"sources_only"`
}

// Derived controls matching a column to an upstream column it no longer shares
// a name with: one that has been aggregated, or packed into or unpacked out of
// a struct.
//
// dbt-osmosis matches on name alone, so a column loses its documentation the
// moment it is summed or nested. Turning this on goes beyond dbt-osmosis, which
// is why it is off by default: parity with an existing dbt-osmosis project is
// the promise, and this would break it.
type Derived struct {
	Enabled *bool `yaml:"enabled"`
	// Structs matches a struct field to the flat column it was packed from, and
	// the other way round, by comparing the last segment of the dotted path.
	// It also expands struct-typed columns into their dotted field names so the
	// fields can be documented at all.
	Structs *bool `yaml:"structs"`
	// Aggregates matches an aggregated column to the column it was computed
	// from by stripping a leading or trailing aggregate word.
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
	// ExpandStructs writes a documentable entry for each field of a struct
	// column, named with dbt's dotted path (`profile.first_name`).
	//
	// This is not an extension: adapters that understand nested data report the
	// fields as columns in their own right, so dbt-osmosis writes them out.
	// dbt-ditto reads catalog.json instead of introspecting, where a struct is
	// a single column with a composite type, so it has to expand the type to
	// arrive at the same answer.
	ExpandStructs *bool `yaml:"expand_structs"`
	// Comments controls using the warehouse's own column comment as the
	// description: "new" (only for a column being added, which is what
	// dbt-osmosis does), "always" (also fill a column that is present but
	// undocumented), or "never".
	//
	// For a source table there is nothing upstream to inherit from, so the
	// warehouse comment is frequently the only documentation in existence.
	Comments *string `yaml:"comments"`
}

// Organize controls YAML file placement.
type Organize struct {
	Enabled *bool `yaml:"enabled"`
	// DeleteEmpty removes schema files left with no entries after a move.
	DeleteEmpty *bool `yaml:"delete_empty"`
}

// Output controls the shape of what is written.
//
// Whether column meta and tags are nested under `config:` is not a setting:
// dbt >= 1.9.6 reads them there and older dbt does not read them there at all,
// so the manifest's own dbt version decides it. Offering a switch could only
// agree with dbt, or silently drop every column's meta.
type Output struct {
	// Comments controls what happens to YAML comments inside a column list
	// when columns are reordered. "follow" (the default) keeps each comment
	// with the column it annotates. "osmosis" reproduces dbt-osmosis, which
	// rebuilds the column list from scratch and so keeps only the comment that
	// sits above the first entry and drops the rest.
	Comments *string `yaml:"comments"`
}

// Sources controls documenting **external** sources — the raw tables no loaded
// dbt project builds — by asking an external program about them.
//
// A source is a root of the DAG, so inheritance can never reach it. The
// warehouse does know about it, but reaching the warehouse needs an SDK,
// credentials and a network call, none of which belong in a binary whose
// premise is reading artifacts off disk. So a provider is a separate program:
// dbt-ditto writes it the sources it wants answers for and reads documentation
// back. See docs/source-providers.md.
type Sources struct {
	Providers []SourceProvider `yaml:"providers"`
	// Strict fails the run when a provider does. Off by default: a tool that
	// tidies YAML should still tidy it when BigQuery is unreachable.
	Strict *bool `yaml:"strict"`
	// Cache is where a refresh writes its answer and where every ordinary run
	// reads it from, relative to the config file. The indirection is what keeps
	// `--check` offline: CI reads a committed or restored cache and spawns
	// nothing, so it needs no warehouse credentials.
	Cache  *string `yaml:"cache"`
	Labels Labels  `yaml:"labels"`
}

// SourceProvider is one external program and the sources it answers for.
type SourceProvider struct {
	// Command is run through the platform shell, from the config file's
	// directory.
	Command string `yaml:"command"`
	// Match limits which sources this provider is asked about, so a project
	// with tables in two warehouses can name a provider for each. Empty
	// patterns match everything; the first provider to claim a source wins.
	Match SourceMatch `yaml:"match"`
}

// SourceMatch selects sources by where they live, with `*` globs.
type SourceMatch struct {
	Database string `yaml:"database"`
	Schema   string `yaml:"schema"`
}

// Labels routes the key-value metadata a provider reports into dbt's own
// vocabulary.
//
// Warehouses agree that objects carry key-value pairs — BigQuery labels,
// Snowflake and Unity Catalog tags, Glue table parameters — and agree on almost
// nothing else. That makes labels the one mapping worth making here rather than
// in each provider: the alternative is every provider inventing its own config
// for the same decision.
type Labels struct {
	// Mode is where the pairs land: "meta" (the default), "tags", "both" or
	// "ignore".
	//
	// Meta is the default because a dbt tag is a *selector*. Turning every
	// warehouse label into a tag would silently change what `--select tag:...`
	// matches in a project that never asked for it.
	Mode *string `yaml:"mode"`
	// MetaKey nests the pairs under one meta key, which keeps them
	// distinguishable from meta written by hand — and is what makes the
	// propagation rules below possible at all. Setting it empty flattens the
	// pairs into meta directly, at the cost of those rules: a flattened label is
	// indistinguishable from any other meta key and inherits like one.
	MetaKey *string `yaml:"meta_key"`
	// TagFormat renders a pair as a tag. A pair with an empty value renders as
	// the bare key whatever this says, because BigQuery permits valueless
	// labels and `owner:` is not a useful tag.
	TagFormat *string `yaml:"tag_format"`
	// Include, when non-empty, is the only label keys accepted. Exclude then
	// drops from what remains. Both match with `*` globs.
	Include []string `yaml:"include"`
	Exclude []string `yaml:"exclude"`

	Propagate LabelPropagate `yaml:"propagate"`
}

// LabelPropagate decides which labels travel down the DAG with the column.
//
// The split is by the level the metadata sits at, which gets the right answer
// for both cases without anyone maintaining a list of keys.
type LabelPropagate struct {
	// There is deliberately no `table` key here. A relation's own labels — who
	// owns it, which cost centre pays for it, which Terraform stack built it —
	// describe the physical object and stop being true the moment they are
	// copied onto a model in another dataset. They never travel, and they
	// cannot: node meta is not inherited by anything in this tool. The level
	// the metadata sits at decides it, so there is nothing to configure.
	//
	// Column labels describe the data in the column. An access rule has no
	// reason to change as the column moves between projects, so they travel
	// like a description. On by default.
	//
	// Turning it off relies on the labels being nested under MetaKey, which is
	// what makes them identifiable: the key joins the skip list inheritance
	// already consults. Labels routed to tags travel regardless — a dbt tag is
	// a selector, and dbt's own rules carry those downstream.
	Column *bool `yaml:"column"`
	// Structs carries labels across a struct pack or unpack match. On by
	// default: `profile.first_name` holds the same bytes the flat `first_name`
	// did, so a classification that was true of one is true of the other.
	Structs *bool `yaml:"structs"`
	// Aggregates decides what happens at an aggregate match, where the
	// description still applies but the value it classified does not:
	// "inherit", "warn" (the default) or "ignore".
	//
	// Warn, because silence is the dangerous answer. A missing description is
	// visibly incomplete and misleads nobody; a missing classification reads as
	// "this column is not restricted", which is a claim, and a false one. So
	// the label is not written but the run says that `avg_salary` derives from
	// a column somebody marked.
	Aggregates *string `yaml:"aggregates"`
	// OnConflict decides what happens when two ancestors in one generation give
	// a label different values: "warn" (the default, writing nothing), "first"
	// (lowest unique_id, as descriptions do) or "none".
	//
	// Descriptions break that tie alphabetically and say so. For a
	// classification an arbitrary choice between `restricted` and `public` is a
	// failure with consequences, and ranking restrictiveness is not something
	// this tool can do without being told an ordering it has no way to learn.
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

	// Loom turns off dbt_loom.config.yml discovery. It is on by default: if a
	// project already tells dbt-loom where its upstream manifests are, repeating
	// that list here would be a second copy to keep in sync.
	Loom *bool `yaml:"loom"`

	Dir string `yaml:"-"`

	// Notes are non-fatal remarks made while loading, such as a dbt-loom
	// manifest this tool cannot reach. They are printed once, before the run.
	Notes []string `yaml:"-"`
}

// Filenames searched for when no explicit config path is given, in order within
// each directory. PyprojectFilename comes last: a dedicated config file is a
// clearer statement of intent than a table inside a Python packaging file, so a
// directory holding both uses the YAML.
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

// SourceCachePath is where source-provider answers are stored, resolved
// against the config file's own directory so a run from anywhere reads the same
// file.
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

// DefaultSourceCache sits under `target/` because that is already the directory
// dbt fills with generated artifacts and which projects already ignore in git.
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
			// Almost every Python project has a pyproject.toml and almost none
			// of them configure this tool in it. Finding one is only an answer
			// if it actually carries the table; otherwise the search carries on
			// upwards, exactly as if the file were not there.
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

// DefaultAmbiguityKey is the meta key an ambiguity annotation is written under.
// It is namespaced to this tool because dbt-osmosis has no equivalent, so there
// is no existing key to match.
const DefaultAmbiguityKey = "dbt_ditto_ambiguous"

// DefaultDirectivePrefix is the marker dbt-doc-inherit uses, so a project that
// already writes these keeps working when it switches tools.
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

// DefaultLabelMetaKey namespaces provider-reported labels inside meta. It is
// namespaced to this tool because no other tool writes these, so there is no
// existing key to match — and because the propagation rules need the labels to
// stay identifiable once they are in meta.
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

// Nested reports whether labels are kept under their own meta key, which is
// what makes them distinguishable from meta written by hand and so what the
// propagation rules depend on. Flattening is allowed, and costs those rules.
func (l ResolvedLabels) Nested() bool { return l.ToMeta() && l.MetaKey != "" }

// DefaultPlaceholders is dbt-osmosis' placeholder list verbatim: an upstream
// description equal to one of these is treated as no description at all and is
// not inherited. The empty string is always a placeholder.
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
		// dbt-osmosis inherits column knowledge only; model descriptions are
		// left alone. Both node-level knobs are therefore opt-in.
		InheritNodeDescription: boolOr(c.Inheritance.NodeDescription, false),
		InheritMeta:            boolOr(c.Inheritance.Meta, true),
		InheritTags:            boolOr(c.Inheritance.Tags, true),
		CaseInsensitive:        boolOr(c.Inheritance.CaseInsensitive, true),
		Force:                  boolOr(c.Inheritance.Force, false),
		// Where a description came from is recorded by default. An inherited
		// description is the one thing in a schema file nobody wrote, so leaving
		// its origin out makes it impossible to tell copied documentation from
		// documentation that was reviewed. dbt-osmosis leaves this off; a project
		// being compared against it byte for byte has to set `progenitor: false`.
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
		r.Placeholders[normalise(p)] = true
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
	// strOr cannot express "the empty string was asked for", and here it has to:
	// an empty meta_key is the documented way to flatten labels into meta.
	r.SourceLabels.MetaKey = DefaultLabelMetaKey
	if l.MetaKey != nil {
		r.SourceLabels.MetaKey = *l.MetaKey
	}
	// Labels that must not travel are handled by the mechanism that already
	// exists for meta nobody wants inherited, rather than by a second one.
	if !r.SourceLabels.PropagateColumn && r.SourceLabels.Nested() {
		r.SkipMetaKeys[r.SourceLabels.MetaKey] = true
	}

	r.Backfill = boolOr(c.Inheritance.Backfill.Enabled, false)
	r.BackfillSourcesOnly = boolOr(c.Inheritance.Backfill.SourcesOnly, true)

	// The master switch gates both strategies, so `derived: {enabled: true}` is
	// enough to get the useful behaviour without listing each one.
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

// DefaultAggregatePrefixes and DefaultAggregateSuffixes are the words analysts
// put around a column name when they aggregate it. `total_amount_cents` and
// `amount_cents_sum` both mean the same quantity as `amount_cents`, so a match
// on the stripped name carries the documentation across.
//
// Only whole underscore-separated words are stripped, so `count_of_things` does
// not quietly become `of_things` for the wrong reason.
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
