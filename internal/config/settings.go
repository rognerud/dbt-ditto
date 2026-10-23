package config

import (
	"fmt"
	"io"
	"strings"
)

// Setting is one configurable key, its default, and what it does. The table
// below is what `dbt-ditto help config` prints, and
// TestEverySettingIsExplained compares it against the config structs, so a key
// cannot be added without an explanation or kept after being removed.
//
// The explanations are one sentence each and say what the setting does, not
// what it is called. The long form, with a worked example per setting, is
// docs/configuration.md.
type Setting struct {
	// Key is the dotted YAML path, with `[]` where a list of entries is indexed.
	Key string
	// Default is how the value reads when the key is absent, in prose where the
	// real default is a list or depends on something else.
	Default string
	// Help is the one sentence shown beneath the key.
	Help string
}

// Settings is every key, in the order the config structs declare them, which
// is also the order a reader of the YAML meets them.
var Settings = []Setting{
	{"projects[].path", "required", "Directory of a dbt project taking part, relative to this file."},
	{"projects[].target", "<path>/target", "Where that project's manifest.json and catalog.json were written."},
	{"projects[].manifest", "none", "A manifest.json with no checkout behind it, which is how a dbt-loom upstream arrives; implies upstream."},
	{"projects[].upstream", "false", "Read this project for its documentation and never write to it."},

	{"inheritance.columns", "true", "Copy a column's description, and its meta and tags, down from the parent column it was built from."},
	{"inheritance.node_description", "false", "Also copy a model's own description from its single parent; dbt-osmosis does not, so this is opt-in."},
	{"inheritance.node.meta", "false", "Carry the table's own meta down the DAG, merged key by key so what the table says about itself is never replaced."},
	{"inheritance.node.tags", "false", "Carry the table's own tags down the DAG, as an order-preserving union with the local ones first."},
	{"inheritance.node.from_parents", "true", "Read the ancestor models, seeds and snapshots for that meta and those tags."},
	{"inheritance.node.from_folders", "true", "Read `+meta:` and `+tags:` from dbt_project.yml, the weakest of the three origins."},
	{"inheritance.node.from_sources", "true", "Read the source tables a model is built from, a source being an ancestor like any other."},

	{"inheritance.meta", "true", "Include the column's meta in what is inherited, key order kept."},
	{"inheritance.tags", "true", "Include the column's tags, merged as an order-preserving union with the local ones first."},
	{"inheritance.case_insensitive", "true", "Match a column to its parent ignoring case, as warehouses that upper-case names require."},
	{"inheritance.force", "false", "Overwrite a description somebody wrote by hand with the inherited one."},
	{"inheritance.placeholders", "dbt-osmosis' five", "Descriptions that count as undocumented, and so are neither inherited nor protected; compared exactly, with no trimming or case folding."},
	{"inheritance.progenitor", "true", "Record the node a description was inherited from in the column's meta; on here, unlike dbt-osmosis, because nobody wrote that line by hand."},
	{"inheritance.progenitor_key", DefaultProgenitorKey, "The meta key that record goes under."},
	{"inheritance.skip_meta_keys", "none", "Meta keys that never travel down the DAG, for the ones that describe ownership rather than data."},
	{"inheritance.extra_keys", "none", "Column keys dbt does not model, such as policy_tags, to carry down with the description."},
	{"inheritance.directives", "true", "Honour `description: \"Inherited: model.column\"` as a pointer at the column to copy from."},
	{"inheritance.directive_prefix", DefaultDirectivePrefix, "The prefix that marks such a pointer; dbt-doc-inherit's marker, so those projects work unchanged."},
	{"inheritance.warn_ambiguous", "true", "Warn when several parents document one column differently, since unique_id order silently picks the winner."},
	{"inheritance.ambiguity_meta", "false", "Also record that disagreement in the column's meta, so it outlives the warning."},
	{"inheritance.ambiguity_key", DefaultAmbiguityKey, "The meta key that record goes under."},

	{"inheritance.derived.enabled", "false", "Match a column to an upstream one whose name it no longer shares, which is a guess and so off by default."},
	{"inheritance.derived.structs", "true", "Within that, match a struct field to the flat column it was packed from, by the last segment of the dotted path."},
	{"inheritance.derived.aggregates", "true", "Within that, match by stripping a leading or trailing aggregate word, so total_amount finds amount."},
	{"inheritance.derived.prefixes", "sum, total, avg, …", "Replace the built-in list of leading aggregate words."},
	{"inheritance.derived.suffixes", "sum, total, avg, …", "Replace the built-in list of trailing aggregate words."},

	{"inheritance.backfill.enabled", "false", "Carry documentation up the DAG from a node's descendants, the only way to document a source; strictly additive, filling blanks only."},
	{"inheritance.backfill.sources_only", "true", "Limit that to source tables, rather than filling in every undocumented model from below."},

	{"columns.add_missing", "true", "Add a column the catalog reports but the YAML does not list."},
	{"columns.remove_stale", "true", "Remove a column the YAML lists but the warehouse no longer has."},
	{"columns.data_types", "true", "Write each column's data_type as the catalog reports it."},
	{"columns.case", "preserve", "Case to write column names in: lower, upper or preserve."},
	{"columns.order", "catalog", "Order to write columns in: catalog (the warehouse's), yaml (leave as found) or alphabetical."},
	{"columns.expand_structs", "true", "Write an entry per struct field under dbt's dotted path, which is what dbt-osmosis' adapter reports."},
	{"columns.comments", "new", "Use the warehouse's own column comment as a description: new (only when undocumented), always or never."},

	{"organize.enabled", "true", "Move a model's entry into the schema file its `+dbt-osmosis:` or `+dbt-ditto-path:` rule names."},
	{"organize.delete_empty", "true", "Delete a schema file left with no entries after such a move."},

	{"output.comments", "follow", "YAML comments: follow keeps each comment with the column it annotates, osmosis keeps only the one above the first entry, as dbt-osmosis does."},

	{"sources.providers[].command", "required", "A program, run through the shell from this file's directory, that reports the columns of raw tables no dbt project builds."},
	{"sources.providers[].match.database", "every database", "Glob limiting which sources this provider is asked about."},
	{"sources.providers[].match.schema", "every schema", "Glob limiting the same by schema."},
	{"sources.strict", "false", "Fail the run when a provider fails, rather than warning and carrying on with the cache."},
	{"sources.cache", DefaultSourceCache, "Where provider output is cached, relative to this file; reading it is what keeps --check offline."},

	{"sources.labels.mode", "meta", "Where a provider's key-value metadata goes: meta, tags, both or ignore."},
	{"sources.labels.meta_key", DefaultLabelMetaKey, "Meta key the pairs nest under, which is what keeps them distinguishable from meta somebody typed; empty flattens them."},
	{"sources.labels.tag_format", DefaultTagFormat, "How a pair renders as a tag; a pair with no value renders as the bare key."},
	{"sources.labels.include", "every key", "When set, the only label keys accepted, matched with globs."},
	{"sources.labels.exclude", "none", "Label keys dropped from whatever include left, matched with globs."},
	{"sources.labels.propagate.column", "true", "Let column labels travel down the DAG like a description, labels on the data being about the data."},
	{"sources.labels.propagate.structs", "true", "Carry labels across a struct pack or unpack, the same bytes reshaped."},
	{"sources.labels.propagate.aggregates", "warn", "What happens to a label at an aggregate match: inherit, warn or ignore."},
	{"sources.labels.propagate.on_conflict", "warn", "When generations disagree about a value: warn and write nothing, first (lowest unique_id, as descriptions do) or none."},

	{"tags.folders", "off", "Read `+tags:` from dbt_project.yml and treat them as canonical for the nodes below: off, rules (a folder tag may only decide a rule's key) or all."},
	{"tags.rules[].key", "required", "A label key whose values form a ladder, so a column fed by several ancestors ends with one value rather than the union."},
	{"tags.rules[].values", "required", "That key's vocabulary, weakest first; a value outside it is reported and left alone."},
	{"tags.rules[].resolve", "strongest", "Which ancestor's value wins: strongest, weakest or first (nearest generation)."},
	{"tags.rules[].inherit", "cut", "How far up to read: cut stops at the nearest ancestor that declares the key, all reads every ancestor so an upstream classification cannot be weakened."},
	{"tags.rules[].default", "none", "Written when no ancestor carries the key at all; like any resolved value, a later run may replace it."},
	{"tags.rules[].write", "tags", "Where the resolved value goes: tags, meta or both."},

	{"loom", "true", "Read each project's dbt_loom.config.yml for its upstream manifests, so that list is not maintained twice."},
}

// SettingsHelp writes every setting, grouped as the YAML nests them. It is a
// command's output rather than a document: the section headings are the keys a
// reader types, and the defaults are what they get by typing nothing.
func SettingsHelp(w io.Writer) error {
	var b strings.Builder
	b.WriteString("dbt-ditto configuration\n\n")
	b.WriteString("Read from " + strings.Join(Filenames[:3], ", ") + ",\n")
	b.WriteString("or a [" + PyprojectTable + "] table in " + PyprojectFilename + ", searched for upwards\n")
	b.WriteString("from the working directory. A single project needs none of this:\n")
	b.WriteString("`dbt-ditto inherit <dir>` uses the `+dbt-osmosis:` rules already in its\n")
	b.WriteString("dbt_project.yml, and `dbt-ditto init` writes a starter file.\n\n")
	b.WriteString("Every key is optional. The defaults below reproduce dbt-osmosis byte for byte,\n")
	b.WriteString("except inheritance.progenitor, which records where a description came from.\n")

	section := "\x00"
	for _, s := range Settings {
		leaf, parent := s.Key, ""
		if i := strings.LastIndex(s.Key, "."); i >= 0 {
			leaf, parent = s.Key[i+1:], s.Key[:i]
		}
		if parent != section {
			section = parent
			heading := parent + ":"
			if parent == "" {
				heading = "(top level)"
			}
			b.WriteString("\n" + heading + "\n")
		}
		// The default sits on the key's line because it is the shortest answer to
		// "what happens if I leave this out", and the sentence explains the rest.
		fmt.Fprintf(&b, "  %-22s [%s]\n", leaf, s.Default)
		for _, line := range wrap(s.Help, 68) {
			b.WriteString("      " + line + "\n")
		}
	}
	b.WriteString("\nOne worked example per setting, executed on every build:\n")
	b.WriteString("https://github.com/rognerud/dbt-ditto/blob/main/docs/configuration.md\n")
	_, err := io.WriteString(w, b.String())
	return err
}

// wrap breaks a sentence on spaces at width, so the help reads in an 80-column
// terminal without the caller counting characters.
func wrap(s string, width int) []string {
	words := strings.Fields(s)
	if len(words) == 0 {
		return nil
	}
	lines := []string{words[0]}
	for _, w := range words[1:] {
		last := len(lines) - 1
		if len(lines[last])+1+len(w) <= width {
			lines[last] += " " + w
			continue
		}
		lines = append(lines, w)
	}
	return lines
}
