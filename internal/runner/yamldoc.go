package runner

import (
	"fmt"
	"strings"

	"github.com/rognerud/dbt-ditto/internal/config"
	"github.com/rognerud/dbt-ditto/internal/dbt"
	"github.com/rognerud/dbt-ditto/internal/inherit"
	"github.com/rognerud/dbt-ditto/internal/yamlfile"
	"gopkg.in/yaml.v3"
)

// writeOpts is the subset of resolved configuration the YAML writer needs,
// plus the per-project decisions that cannot live in the config (whether this
// manifest's dbt understands column `config:` blocks).
type writeOpts struct {
	cfg         config.Resolved
	configBlock bool
}

// readExisting reads the node's current documentation out of a schema file.
func readExisting(f *yamlfile.File, n *dbt.Node) inherit.Existing {
	entry := findEntry(f, n)
	if entry == nil {
		return inherit.Existing{}
	}
	ex := inherit.Existing{
		Present:     true,
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
// both YAML shapes are understood on the way in.
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
		if !contains(tags, t) {
			tags = append(tags, t)
		}
	}
	return tags
}

func contains(hay []string, needle string) bool {
	for _, h := range hay {
		if h == needle {
			return true
		}
	}
	return false
}

// writeDoc applies a resolved NodeDoc to the destination file and returns a
// human-readable list of what it changed.
func writeDoc(f *yamlfile.File, n *dbt.Node, doc *inherit.NodeDoc, opts writeOpts) []string {
	if !doc.Changed() && findEntry(f, n) == nil && len(doc.Columns) == 0 {
		return nil
	}
	entry := ensureEntry(f, n)
	var changes []string

	if doc.SetDescription {
		yamlfile.MapSet(entry, "description", yamlfile.Scalar(doc.Description))
		changes = append(changes, fmt.Sprintf("description inherited from %s", doc.DescriptionFrom))
	}
	if len(doc.Meta) > 0 {
		// Node-level meta keeps the classic top-level shape: dbt-osmosis only
		// moves *column* meta into a config block.
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
			existingCols[fold(yamlfile.StringOf(yamlfile.MapGet(c, "name")), opts.cfg)] = c
		}
	}

	dropped := map[string]bool{}
	for _, d := range doc.Drop {
		dropped[fold(d, opts.cfg)] = true
	}

	newSeq := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
	kept := map[string]bool{}

	for _, cd := range doc.Columns {
		key := fold(cd.Name, opts.cfg)
		node := existingCols[key]
		if node == nil {
			node = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
			yamlfile.MapSet(node, "name", yamlfile.Scalar(cd.Name))
			changes = append(changes, fmt.Sprintf("+ column %s", cd.Name))
		}
		kept[key] = true

		if cd.SetDescription {
			yamlfile.MapSet(node, "description", yamlfile.Scalar(cd.Description))
			// A description with no progenitor was not inherited from anywhere:
			// it is the manifest's own text being written into the file.
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
		// Extra keys are dbt's own column keys, so they are written where dbt
		// reads them — beside `name`, not inside `meta` or `config`.
		for _, e := range cd.Extra {
			v, err := yamlfile.Encode(e.Value)
			if err != nil {
				continue
			}
			yamlfile.MapSet(node, e.Key, v)
		}
		// dbt-osmosis rebuilds each column entry from scratch and always ends
		// it with the config block. Editing in place instead keeps `config`
		// wherever it already was, which puts a newly added `data_type` after
		// it; on a project whose YAML already uses config blocks that is a
		// gratuitous diff on the first run.
		moveKeyLast(node, "config")

		newSeq.Content = append(newSeq.Content, node)
	}

	// Anything the resolver neither kept nor explicitly dropped stays put, so
	// disabling remove_stale really does leave columns alone.
	for _, c := range existingOrder {
		key := fold(yamlfile.StringOf(yamlfile.MapGet(c, "name")), opts.cfg)
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

// dropItemComments reproduces dbt-osmosis' handling of comments inside a column
// list. dbt-osmosis rebuilds the list as a fresh Python list, so ruamel loses
// every comment attached to an item; the only survivor is the comment above the
// first entry, which ruamel files against the `columns:` key rather than the
// item. Reproducing that loss is opt-in (output.comments: osmosis) and exists so
// the two tools can be compared byte for byte.
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

// setMeta writes the complete meta mapping, at the top level or inside a
// `config:` block, and removes whichever of the two is not in use so a column
// never ends up carrying both.
func setMeta(entry *yaml.Node, meta []inherit.MetaEntry, configBlock bool) {
	if len(meta) == 0 {
		yamlfile.MapDelete(entry, "meta")
		deleteFromConfig(entry, "meta")
		return
	}

	holder := entry
	if configBlock {
		yamlfile.MapDelete(entry, "meta")
		holder = ensureConfig(entry)
	} else {
		deleteFromConfig(entry, "meta")
	}

	m := yamlfile.MapGet(holder, "meta")
	if m == nil || m.Kind != yaml.MappingNode {
		m = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		yamlfile.MapSet(holder, "meta", m)
	}
	// Rebuild in the resolved order, reusing existing value nodes so any
	// comments and quoting style on them survive.
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
	rebuilt.HeadComment, rebuilt.LineComment, rebuilt.FootComment = m.HeadComment, m.LineComment, m.FootComment
	yamlfile.MapSet(holder, "meta", rebuilt)
}

// setTags writes the complete tag list, top level or inside `config:`.
func setTags(entry *yaml.Node, tags []string, configBlock bool) {
	if len(tags) == 0 {
		yamlfile.MapDelete(entry, "tags")
		deleteFromConfig(entry, "tags")
		return
	}

	holder := entry
	if configBlock {
		yamlfile.MapDelete(entry, "tags")
		holder = ensureConfig(entry)
	} else {
		deleteFromConfig(entry, "tags")
	}

	seq := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
	for _, t := range tags {
		seq.Content = append(seq.Content, yamlfile.Scalar(t))
	}
	yamlfile.MapSet(holder, "tags", seq)
}

// moveKeyLast shifts a key, and its value, to the end of a mapping, leaving the
// order of everything else alone.
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

func ensureConfig(entry *yaml.Node) *yaml.Node {
	cfg := yamlfile.MapGet(entry, "config")
	if cfg == nil || cfg.Kind != yaml.MappingNode {
		cfg = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		yamlfile.MapSet(entry, "config", cfg)
	}
	return cfg
}

// deleteFromConfig removes a key from the `config:` block, dropping the block
// itself once it is empty.
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

func fold(s string, cfg config.Resolved) string {
	if cfg.CaseInsensitive {
		return strings.ToLower(s)
	}
	return s
}

func metaKeys(meta []inherit.MetaEntry) string {
	out := make([]string, 0, len(meta))
	for _, e := range meta {
		out = append(out, e.Key)
	}
	return strings.Join(out, ",")
}
