// Package canon rewrites dbt schema YAML with its top-level entries sorted by
// name, so that two trees can be compared without the comparison depending on
// the order dbt happened to list its nodes in.
//
// dbt does not order the nodes in a manifest deterministically, and that order
// is visible in the bytes whenever several nodes share one schema file:
// dbt-ditto writes entries in manifest order because dbt-osmosis does. The two
// tools learn the order by different routes, though — dbt-osmosis parses the
// project on every run, dbt-ditto reads the committed target/manifest.json — so
// a byte comparison of the two outputs would otherwise assert an order neither
// tool promises, and flip with the machine that ran it.
//
// Entry order within a file is a separate claim, proved by the tests that run
// against the recorded manifest.
package canon

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/rognerud/dbt-ditto/internal/yamlfile"
	"gopkg.in/yaml.v3"
)

// EntryKeys are the top-level dbt schema sequences whose entries are named and
// whose order carries no meaning.
var EntryKeys = []string{
	"models", "seeds", "snapshots", "sources", "analyses", "exposures", "macros",
}

// skipNames are the project files that are configuration rather than schema.
var skipNames = map[string]bool{
	"dbt_project.yml":     true,
	"profiles.yml":        true,
	"packages.yml":        true,
	"dependencies.yml":    true,
	".user.yml":           true,
	"dbt_ditto.yml":       true,
	"dbt_loom.config.yml": true,
	"dbt_osmosis.yml":     true,
}

// skipDirs are the generated directories that hold no hand-editable schema.
var skipDirs = map[string]bool{
	"target":       true,
	"logs":         true,
	"dbt_packages": true,
}

// Dir canonicalises every schema file under root, in place.
func Dir(root string) error {
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if skipDirs[d.Name()] {
				return fs.SkipDir
			}
			return nil
		}
		if !IsSchemaFile(d.Name()) {
			return nil
		}
		return File(path)
	})
}

// IsSchemaFile reports whether a file name is dbt schema YAML.
func IsSchemaFile(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".yml", ".yaml":
	default:
		return false
	}
	return !skipNames[name]
}

// File canonicalises one schema file, in place.
//
// The file is always written back, even when no entry moved: a file rewritten
// on one side of a comparison and left as it was read on the other would
// compare the encoder's formatting against the original bytes.
func File(path string) error {
	f, err := yamlfile.Load(path)
	if err != nil {
		return err
	}
	Document(f)
	out, err := f.Render()
	if err != nil {
		return err
	}
	return os.WriteFile(path, out, 0o644)
}

// Document sorts the named entries of a loaded document.
func Document(f *yamlfile.File) {
	for _, key := range EntryKeys {
		seq := f.Seq(key, false)
		if seq == nil {
			continue
		}
		sortByName(seq)
		if key == "sources" {
			// A source's tables are named entries in the same way.
			for _, src := range seq.Content {
				sortByName(yamlfile.MapGet(src, "tables"))
			}
		}
	}
}

// sortByName orders a sequence of `name:`-keyed mappings alphabetically.
// Anything that is not such a mapping keeps its place relative to its
// neighbours, which is what a stable sort on an empty key gives.
func sortByName(seq *yaml.Node) {
	if seq == nil || seq.Kind != yaml.SequenceNode {
		return
	}
	sort.SliceStable(seq.Content, func(i, j int) bool {
		return nameOf(seq.Content[i]) < nameOf(seq.Content[j])
	})
}

func nameOf(n *yaml.Node) string {
	if n == nil || n.Kind != yaml.MappingNode {
		return ""
	}
	return yamlfile.StringOf(yamlfile.MapGet(n, "name"))
}
