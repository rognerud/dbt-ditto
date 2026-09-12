package runner

import (
	"fmt"
	"slices"
	"strings"

	"github.com/rognerud/dbt-ditto/internal/config"
	"github.com/rognerud/dbt-ditto/internal/dbt"
	"github.com/rognerud/dbt-ditto/internal/inherit"
	"github.com/rognerud/dbt-ditto/internal/yamlfile"
	"gopkg.in/yaml.v3"
)

// writeOpts is the resolved configuration the YAML writer needs, plus the
// per-project decisions that cannot live in the config.
type writeOpts struct {
	cfg         config.Resolved
	configBlock bool
}

// readExisting reads the node's current documentation out of a schema file.
func readExisting(f *yamlfile.File, n *dbt.Node) inherit.Existing {
	entry := entryFor(f, n, false)
	if entry == nil {
		return inherit.Existing{}
	}
	ex := inherit.Existing{
		Description: yamlfile.StringOf(yamlfile.MapGet(entry, "description")),
		Meta:        readMeta(entry),
	}
	if cols := yamlfile.MapGet(entry, "columns"); cols != nil && cols.Kind == yaml.SequenceNode {
		for _, c := range cols.Content {
			if c.Kind != yaml.MappingNode {
				continue
			}
			ex.Columns = append(ex.Columns, inherit.ExistingColumn{
				Name:        yamlfile.StringOf(yamlfile.MapGet(c, "name")),
				Description: yamlfile.StringOf(yamlfile.MapGet(c, "description")),
				Meta:        readMeta(c),
				Tags:        readTags(c),
			})
		}
	}
	return ex
}

// readMeta reads `meta:` and `config.meta:` off an entry, in that order, so
func readMeta(entry *yaml.Node) []inherit.MetaEntry {
	var out []inherit.MetaEntry
	seen := map[string]int{}
	add := func(m *yaml.Node) {
		if m == nil || m.Kind != yaml.MappingNode {
			return
		}
		for i := 0; i+1 < len(m.Content); i += 2 {
			key := m.Content[i].Value
			var val any
			if err := m.Content[i+1].Decode(&val); err != nil {
				continue
			}
			if idx, ok := seen[key]; ok {
				out[idx].Value = val
				continue
			}
			seen[key] = len(out)
			out = append(out, inherit.MetaEntry{Key: key, Value: val})
		}
	}
	add(yamlfile.MapGet(entry, "meta"))
	add(yamlfile.MapGet(yamlfile.MapGet(entry, "config"), "meta"))
	return out
}

// readTags reads `tags:` and `config.tags:` off an entry.
func readTags(entry *yaml.Node) []string {
	tags := yamlfile.DecodeStrings(yamlfile.MapGet(entry, "tags"))
	for _, t := range yamlfile.DecodeStrings(yamlfile.MapGet(yamlfile.MapGet(entry, "config"), "tags")) {
		if !slices.Contains(tags, t) {
			tags = append(tags, t)
		}
	}
	return tags
}

// writeDoc applies a resolved NodeDoc to the destination file and returns a
func writeDoc(f *yamlfile.File, n *dbt.Node, doc *inherit.NodeDoc, opts writeOpts) []string {
	if !doc.Changed() && entryFor(f, n, false) == nil && len(doc.Columns) == 0 {
		return nil
	}
	entry := entryFor(f, n, true)
	var changes []string

	if doc.SetDescription {
		yamlfile.MapSet(entry, "description", yamlfile.Scalar(doc.Description))
		changes = append(changes, fmt.Sprintf("description inherited from %s", doc.DescriptionFrom))
	}
	if len(doc.Meta) > 0 {
		// Node-level meta keeps the top-level shape: dbt-osmosis only moves *column*
		// meta into a config block.
		setMeta(entry, doc.Meta, false)
		changes = append(changes, fmt.Sprintf("meta = %s", metaKeys(doc.Meta)))
	}

	if !opts.cfg.InheritColumns {
		return changes
	}

	existingCols := map[string]*yaml.Node{}
	var existingOrder []*yaml.Node
	if cols := yamlfile.MapGet(entry, "columns"); cols != nil && cols.Kind == yaml.SequenceNode {
		for _, c := range cols.Content {
			if c.Kind != yaml.MappingNode {
				continue
			}
			existingOrder = append(existingOrder, c)
			existingCols[opts.cfg.Fold(yamlfile.StringOf(yamlfile.MapGet(c, "name")))] = c
		}
	}

	dropped := map[string]bool{}
	for _, d := range doc.Drop {
		dropped[opts.cfg.Fold(d)] = true
	}

	newSeq := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
	kept := map[string]bool{}

	for _, cd := range doc.Columns {
		key := opts.cfg.Fold(cd.Name)
		node := existingCols[key]
		if node == nil {
			node = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
			yamlfile.MapSet(node, "name", yamlfile.Scalar(cd.Name))
			changes = append(changes, fmt.Sprintf("+ column %s", cd.Name))
		}
		kept[key] = true

		if cd.SetDescription {
			yamlfile.MapSet(node, "description", yamlfile.Scalar(cd.Description))
			// A description with no progenitor was not inherited: it is the manifest's own
			// text being written into the file.
			if cd.Progenitor != "" {
				changes = append(changes, fmt.Sprintf("%s.description inherited from %s", cd.Name, cd.Progenitor))
			} else {
				changes = append(changes, fmt.Sprintf("%s.description written", cd.Name))
			}
		} else if yamlfile.StringOf(yamlfile.MapGet(node, "description")) == "" {
			// An empty description is noise; dbt-osmosis does not write one.
			yamlfile.MapDelete(node, "description")
		}
		if cd.SetDataType {
			yamlfile.MapSet(node, "data_type", yamlfile.Scalar(cd.DataType))
		}
		setMeta(node, cd.Meta, opts.configBlock)
		setTags(node, cd.Tags, opts.configBlock)
		// Extra keys are dbt's own column keys, so they go where dbt reads them.
		for _, e := range cd.Extra {
			v, err := yamlfile.Encode(e.Value)
			if err != nil {
				continue
			}
			yamlfile.MapSet(node, e.Key, v)
		}
		// dbt-osmosis rebuilds each column entry and always ends it with the config block.
		moveKeyLast(node, "config")

		newSeq.Content = append(newSeq.Content, node)
	}

	// Anything the resolver neither kept nor dropped stays put, so disabling
	// remove_stale really does leave columns alone.
	for _, c := range existingOrder {
		key := opts.cfg.Fold(yamlfile.StringOf(yamlfile.MapGet(c, "name")))
		if kept[key] {
			continue
		}
		if dropped[key] {
			changes = append(changes, fmt.Sprintf("- column %s", yamlfile.StringOf(yamlfile.MapGet(c, "name"))))
			continue
		}
		newSeq.Content = append(newSeq.Content, c)
	}

	if opts.cfg.Comments == config.CommentsOsmosis {
		dropItemComments(existingOrder, newSeq.Content)
	}

	if len(newSeq.Content) > 0 {
		yamlfile.MapSet(entry, "columns", newSeq)
	} else {
		yamlfile.MapDelete(entry, "columns")
	}
	return changes
}

// dropItemComments reproduces dbt-osmosis' comment loss inside a column list:
// it rebuilds the list as a fresh Python list, so ruamel loses every comment
// attached to an item except the one above the first entry, which it files
// against the `columns:` key. Opt-in, so the two tools compare byte for byte.
func dropItemComments(old, updated []*yaml.Node) {
	var lead string
	if len(old) > 0 {
		lead = old[0].HeadComment
	}
	for _, n := range updated {
		n.HeadComment, n.LineComment, n.FootComment = "", "", ""
	}
	if lead != "" && len(updated) > 0 {
		updated[0].HeadComment = lead
	}
}

// setMeta writes the complete meta mapping, top level or inside `config:`, and
// removes the other so a column never carries both.
func setMeta(entry *yaml.Node, meta []inherit.MetaEntry, configBlock bool) {
	holder := holderFor(entry, "meta", configBlock, len(meta) > 0)
	if holder == nil {
		return
	}

	m := yamlfile.MapGet(holder, "meta")
	if m != nil && m.Kind != yaml.MappingNode {
		m = nil // whatever was there is not a mapping, so nothing to reuse
	}
	// Rebuild in the resolved order, reusing existing value nodes so comments and
	// quoting style survive.
	rebuilt := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	for _, e := range meta {
		v, err := yamlfile.Encode(e.Value)
		if err != nil {
			continue
		}
		if old := yamlfile.MapGet(m, e.Key); old != nil {
			v.HeadComment, v.LineComment, v.FootComment = old.HeadComment, old.LineComment, old.FootComment
		}
		yamlfile.MapSet(rebuilt, e.Key, v)
	}
	// MapSet carries the comments of whatever `meta:` held before onto the rebuild.
	yamlfile.MapSet(holder, "meta", rebuilt)
}

// setTags writes the complete tag list, top level or inside `config:`.
func setTags(entry *yaml.Node, tags []string, configBlock bool) {
	holder := holderFor(entry, "tags", configBlock, len(tags) > 0)
	if holder == nil {
		return
	}

	seq := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
	for _, t := range tags {
		seq.Content = append(seq.Content, yamlfile.Scalar(t))
	}
	yamlfile.MapSet(holder, "tags", seq)
}

// moveKeyLast shifts a key, and its value, to the end of a mapping, leaving the
func moveKeyLast(m *yaml.Node, key string) {
	if m == nil || m.Kind != yaml.MappingNode {
		return
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value != key {
			continue
		}
		if i+2 == len(m.Content) {
			return // already last
		}
		k, v := m.Content[i], m.Content[i+1]
		m.Content = append(m.Content[:i], m.Content[i+2:]...)
		m.Content = append(m.Content, k, v)
		return
	}
}

// holderFor clears key from whichever of the entry and its `config:` block does
// not own it and returns the one that does, or nil when there is nothing to write.
func holderFor(entry *yaml.Node, key string, configBlock, want bool) *yaml.Node {
	if !want || configBlock {
		yamlfile.MapDelete(entry, key)
	}
	if !want || !configBlock {
		deleteFromConfig(entry, key)
	}
	switch {
	case !want:
		return nil
	case configBlock:
		return ensureConfig(entry)
	}
	return entry
}

func ensureConfig(entry *yaml.Node) *yaml.Node {
	cfg := yamlfile.MapGet(entry, "config")
	if cfg == nil || cfg.Kind != yaml.MappingNode {
		cfg = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		yamlfile.MapSet(entry, "config", cfg)
	}
	return cfg
}

// deleteFromConfig removes a key from the `config:` block, dropping the block
func deleteFromConfig(entry *yaml.Node, key string) {
	cfg := yamlfile.MapGet(entry, "config")
	if cfg == nil || cfg.Kind != yaml.MappingNode {
		return
	}
	yamlfile.MapDelete(cfg, key)
	if len(cfg.Content) == 0 {
		yamlfile.MapDelete(entry, "config")
	}
}

func metaKeys(meta []inherit.MetaEntry) string {
	out := make([]string, 0, len(meta))
	for _, e := range meta {
		out = append(out, e.Key)
	}
	return strings.Join(out, ",")
}
