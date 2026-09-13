package inherit

import (
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/rognerud/dbt-ditto/internal/config"
	"github.com/rognerud/dbt-ditto/internal/dbt"
)

// MetaEntry is one key of a column's `meta`, kept ordered as the analyst wrote it.
type MetaEntry struct {
	Key   string
	Value any
}

// ColumnDoc is the documentation dbt-ditto wants a column to end up with.
type ColumnDoc struct {
	// Name is the spelling to write.
	Name string
	// Existing is true when the column is already present in the YAML file.
	Existing bool

	// SetDescription is false when the column keeps whatever it already has.
	Description    string
	SetDescription bool

	DataType    string
	SetDataType bool

	// Meta and Tags are the complete desired sets, in write order.
	Meta []MetaEntry
	Tags []string
	// Extra are unmodelled keys carried via `inheritance.extra_keys`, in config order.
	Extra []MetaEntry

	// Progenitor is the unique_id the description came from; empty when local.
	Progenitor string
}

// NodeDoc is the resolved documentation for one node.
type NodeDoc struct {
	Node *dbt.Node

	Description     string
	SetDescription  bool
	DescriptionFrom string

	// No node-level Meta: dbt-osmosis inherits a node's description but not its
	// meta, and matching it is the point. Column meta is on ColumnDoc.

	// Columns is the full desired column list, in write order.
	Columns []ColumnDoc
	// Drop lists YAML column names that no longer exist in the warehouse.
	Drop []string
	// Warnings are non-fatal: a dangling directive, or parents that disagree.
	Warnings []Warning
}

// Warning kinds.
const (
	// WarnAmbiguous: parents disagree, so unique_id order picks the winner.
	WarnAmbiguous = "ambiguous"
	// WarnDirective: an `Inherited: model.column` pointer could not be resolved.
	WarnDirective = "directive"
	// WarnShadowedSource: a `source:` shadows a relation a loaded project builds,
	// so it is a graph root and inherits nothing.
	WarnShadowedSource = "shadowed_source"
)

// Warning is one thing the run wants to say about a column without failing.
type Warning struct{ Node, Column, Kind, Detail string }

// warn records something the run wants to say about a column without failing.
func (d *NodeDoc) warn(column, kind, format string, args ...any) {
	d.Warnings = append(d.Warnings, Warning{
		Node: d.Node.UniqueID, Column: column, Kind: kind,
		Detail: fmt.Sprintf(format, args...),
	})
}

// Changed reports whether the resolution asks for any edit at all.
func (d *NodeDoc) Changed() bool {
	if d.SetDescription || len(d.Drop) > 0 {
		return true
	}
	for _, c := range d.Columns {
		if !c.Existing || c.SetDescription || c.SetDataType ||
			len(c.Meta) > 0 || len(c.Tags) > 0 || len(c.Extra) > 0 {
			return true
		}
	}
	return false
}

// Resolver turns a graph plus configuration into per-node documentation.
type Resolver struct {
	Graph *Graph
	Cfg   config.Resolved

	// Definitives are settled descriptions keyed by folded column name; see
	// BuildDefinitives, which also reports conflicts.
	Definitives map[string]Definitive

	matchOnce sync.Once
	match     *matcher
}

// matcher builds the column matcher on first use, so a zero Resolver works.
func (r *Resolver) matcher() *matcher {
	r.matchOnce.Do(func() { r.match = newMatcher(r.Cfg) })
	return r.match
}

// ExistingColumn is the state of a column as currently written in YAML.
type ExistingColumn struct {
	Name, Description string
	Meta              []MetaEntry
	Tags              []string
}

// Existing is the current YAML state of a node, read from the schema file.
// No node-level Meta, for the same reason NodeDoc has none: nothing inherits it,
// so reading it would only be state that could drift.
type Existing struct {
	Description string
	Columns     []ExistingColumn
}

// knowledge accumulates one column's view while walking ancestors, mirroring
// dbt-osmosis' "column knowledge graph" entry.
type knowledge struct {
	description string
	meta        *dbt.OrderedMap
	tags        []string
	// extra holds `inheritance.extra_keys` values; they travel exactly as meta does.
	extra map[string]any
	// notes are warnings raised while folding ancestors in (labels dropped or
	// disagreed about); inheritInto walks generations, Resolve owns the list.
	notes []Warning
}

// note records a warning about the column being folded together; Resolve fills
// in the node it belongs to.
func (k *knowledge) note(column, kind, format string, args ...any) {
	k.notes = append(k.notes, Warning{
		Column: column, Kind: kind, Detail: fmt.Sprintf(format, args...),
	})
}

// Resolve computes the documentation for a node given what its YAML file says
// today.
//
// The algorithm is dbt-osmosis' inherit_upstream_column_knowledge:
//
//   - the column's own metadata seeds the knowledge entry;
//   - ancestors are visited furthest generation first, so nearer ancestors
//     overwrite what further ones contributed;
//   - within a generation the first ancestor (by unique_id) that has the column
//     claims it, and the rest of that generation is skipped for that column;
//   - an ancestor description that is a placeholder never overwrites anything;
//   - the resulting description is applied only when the column has no local
//     description, while meta and tags are always applied.
func (r *Resolver) Resolve(n *dbt.Node, existing Existing) *NodeDoc {
	doc := &NodeDoc{Node: n}
	r.resolveNodeLevel(n, existing, doc)

	if !r.Cfg.InheritColumns {
		return doc
	}

	existingByKey := make(map[string]*ExistingColumn, len(existing.Columns))
	for i := range existing.Columns {
		existingByKey[r.Cfg.Fold(existing.Columns[i].Name)] = &existing.Columns[i]
	}

	// haveTruth says the list is warehouse truth, which alone licenses deletion.
	truth, haveTruth := r.trueColumns(n)
	if len(truth) == 0 {
		// Nothing known about the relation: enrich what the file lists, add nothing.
		for _, c := range existing.Columns {
			truth = append(truth, columnTruth{name: c.Name})
		}
	}

	truthByKey := make(map[string]bool, len(truth))
	for _, t := range truth {
		truthByKey[r.Cfg.Fold(t.name)] = true
	}

	generations := r.Graph.Generations(n.UniqueID)

	for _, t := range r.orderColumns(truth, existing.Columns, haveTruth) {
		key := r.Cfg.Fold(t.name)
		ex := existingByKey[key]
		if ex == nil && !r.Cfg.AddMissing {
			continue
		}

		cd := ColumnDoc{Name: r.applyCase(t.name), Existing: ex != nil}
		if ex != nil {
			cd.Name = ex.Name // never churn an already-written spelling
		}

		k := r.seedKnowledge(n, t.name, ex)
		if k.description == "" && t.comment != "" && r.useComment(n, t.name, ex) {
			// A warehouse comment counts as hand-written documentation: it is kept,
			// and nothing upstream overwrites it.
			k.description = t.comment
		}
		local := k.description

		// A directive says where the description lives; resolve it first.
		var directiveDesc, directiveFrom string
		if directive, ok := r.parseDirective(local); ok {
			var err string
			directiveDesc, directiveFrom, err = r.followDirective(directive)
			if err != "" {
				doc.warn(t.name, WarnDirective, "%q %s", directive, err)
			}
		}

		progenitor, competing := r.inheritInto(&k, generations, t.name)
		for _, w := range k.notes {
			w.Node = n.UniqueID
			doc.Warnings = append(doc.Warnings, w)
		}
		definitive, settled := r.definitiveFor(t.name)
		if settled {
			// Decided in writing, so no longer worth reporting.
			competing = nil
		}
		// competing also feeds the meta annotation below, so gate the warning here.
		if len(competing) > 0 && r.Cfg.WarnAmbiguous {
			doc.warn(t.name, WarnAmbiguous, "documented differently by %s; took %s",
				strings.Join(competing, ", "), progenitor)
		}

		if k.description == "" && r.backfills(n) {
			// Still nothing: look downstream. Only empty descriptions are filled.
			if desc, from, ok := r.backfill(n, t.name); ok {
				k.description, progenitor = desc, from
			}
		}

		// A column keeps its own description; only an empty one inherits.
		final := k.description
		if !r.Cfg.Force && local != "" {
			final, progenitor = local, ""
		}
		// A resolved directive outranks everything, including `force`.
		if directiveDesc != "" {
			final, progenitor = directiveDesc, directiveFrom
		}

		// A definitive outranks even that: the marked column is the decision, so it
		// keeps its wording; every other column of that name takes the settled one,
		// and the disagreement that prompted it stops being reported.
		if settled {
			if r.declaresDefinitive(n, t.name) {
				final, progenitor = local, ""
			} else {
				final, progenitor = definitive.Description, definitive.Node
			}
		}

		if final != "" && (ex == nil || final != ex.Description) {
			cd.Description, cd.SetDescription, cd.Progenitor = final, true, progenitor
		}

		if r.Cfg.DataTypes && t.dataType != "" {
			cd.DataType, cd.SetDataType = t.dataType, true
		}

		if r.Cfg.Progenitor && r.Cfg.ProgenitorKey != "" && progenitor != "" {
			k.meta.Set(r.Cfg.ProgenitorKey, progenitor)
		}

		if r.Cfg.AmbiguityMeta && r.Cfg.AmbiguityKey != "" {
			// Annotate only descriptions actually inherited under disagreement;
			// settling locally clears `progenitor`, so drop the stale annotation.
			if len(competing) > 0 && progenitor != "" {
				k.meta.Set(r.Cfg.AmbiguityKey, append([]string(nil), competing...))
			} else {
				k.meta.Delete(r.Cfg.AmbiguityKey)
			}
		}
		cd.Meta = entries(k.meta)
		cd.Tags = k.tags
		// Configured order, not map order, so a run writes the same bytes twice.
		for _, ek := range r.Cfg.ExtraKeys {
			if v, ok := k.extra[ek]; ok && v != nil {
				cd.Extra = append(cd.Extra, MetaEntry{Key: ek, Value: v})
			}
		}

		doc.Columns = append(doc.Columns, cd)
	}

	if r.Cfg.RemoveStale && haveTruth {
		for _, c := range existing.Columns {
			if !truthByKey[r.Cfg.Fold(c.Name)] {
				doc.Drop = append(doc.Drop, c.Name)
			}
		}
	}
	return doc
}

// backfills reports whether a node may take documentation from downstream.
func (r *Resolver) backfills(n *dbt.Node) bool {
	return r.Cfg.Backfill && (!r.Cfg.BackfillSourcesOnly || n.IsSource())
}

// backfill finds a description among descendants, nearest generation first, and returns
// the node it came from.
func (r *Resolver) backfill(n *dbt.Node, name string) (string, string, bool) {
	m := r.matcher()
	keys := m.keysFor(name)
	for _, generation := range r.Graph.Descendants(n.UniqueID) {
		for _, d := range generation {
			c, _, ok := m.find(d, name, keys)
			if !ok || c == nil {
				continue
			}
			if c.Description != "" && !r.Cfg.IsPlaceholder(c.Description) {
				return c.Description, d.UniqueID, true
			}
		}
	}
	return "", "", false
}

// useComment reports whether a warehouse comment may be a column's description.
func (r *Resolver) useComment(n *dbt.Node, name string, ex *ExistingColumn) bool {
	switch r.Cfg.WarehouseComments {
	case config.WarehouseCommentsNever:
		return false
	case config.WarehouseCommentsAlways:
		return true
	}
	// Newly invented: neither the YAML nor the manifest knows the column.
	return ex == nil && n.Column(name, r.Cfg.CaseInsensitive) == nil
}

// seedKnowledge starts a column's knowledge from the node itself.
func (r *Resolver) seedKnowledge(n *dbt.Node, name string, ex *ExistingColumn) knowledge {
	k := knowledge{meta: dbt.NewOrderedMap()}
	if mc := n.Column(name, r.Cfg.CaseInsensitive); mc != nil {
		k.description = mc.Description
		if m := mc.EffectiveMeta(); m != nil {
			k.meta = m.Clone()
		}
		k.tags = append(k.tags, mc.EffectiveTags()...)
		k.extra = dbt.MergeExtra(nil, mc.Extra, r.Cfg.ExtraKeys, true)
		return k
	}
	if ex != nil {
		k.description = ex.Description
		for _, e := range ex.Meta {
			k.meta.Set(e.Key, e.Value)
		}
		k.tags = append(k.tags, ex.Tags...)
	}
	return k
}

// inheritInto folds every ancestor generation into k, furthest first, and returns the
// unique_id the surviving description came from.
func (r *Resolver) inheritInto(k *knowledge, generations [][]*dbt.Node, name string) (string, []string) {
	progenitor := ""
	var competing []string
	m := r.matcher()
	keys := m.keysFor(name)
	for i := len(generations) - 1; i >= 0; i-- {
		for j, a := range generations[i] {
			c, rank, ok := m.find(a, name, keys)
			if !ok {
				continue
			}
			// First ancestor in the generation with the column claims it; the rest
			// of the generation is skipped, as dbt-osmosis does.
			if r.Cfg.InheritTags {
				k.tags = dbt.UnionTags(k.tags, c.EffectiveTags())
			}
			k.extra = dbt.MergeExtra(k.extra, c.Extra, r.Cfg.ExtraKeys, true)
			if r.Cfg.InheritMeta {
				labelKey := r.labelKey()
				em := c.EffectiveMeta()
				for _, mk := range em.Keys() {
					// An ancestor's annotations describe its own column, not this one.
					if r.Cfg.SkipMetaKeys[mk] || mk == r.Cfg.ProgenitorKey ||
						mk == r.Cfg.AmbiguityKey || mk == DefinitiveKey {
						continue
					}
					v, _ := em.Get(mk)
					if labelKey != "" && mk == labelKey {
						// Labels travel under their own rules: what may be done, not meaning.
						if !r.carryLabels(k, a, generations[i][j+1:], name, keys, rank, v) {
							continue
						}
					}
					k.meta.Set(mk, v)
				}
			}
			if c.Description != "" && !r.Cfg.IsPlaceholder(c.Description) {
				k.description = c.Description
				progenitor = a.UniqueID
				competing = r.dissenters(generations[i][j+1:], name, keys, c.Description)
				// Carry the original progenitor forward, not the intermediate model.
				if m := c.EffectiveMeta(); m != nil {
					if p, ok := m.Get(r.Cfg.ProgenitorKey); ok {
						if s, ok := p.(string); ok && s != "" {
							progenitor = s
						}
					}
				}
			}
			break
		}
	}
	return progenitor, competing
}

// dissenters names ancestors in the rest of a generation that document the
// column differently from the winner.
func (r *Resolver) dissenters(rest []*dbt.Node, name string, keys [matchNone][]string, won string) []string {
	if !r.Cfg.WarnAmbiguous && !r.Cfg.AmbiguityMeta {
		return nil
	}
	var out []string
	r.eachMatch(rest, name, keys, func(a *dbt.Node, c *dbt.Column) bool {
		if c.Description != "" && !r.Cfg.IsPlaceholder(c.Description) && c.Description != won {
			out = append(out, a.UniqueID)
		}
		return true
	})
	return out
}

// eachMatch calls fn for every node in rest that has the column, stopping early
// when fn returns false.
func (r *Resolver) eachMatch(rest []*dbt.Node, name string, keys [matchNone][]string,
	fn func(*dbt.Node, *dbt.Column) bool) {

	m := r.matcher()
	for _, a := range rest {
		c, _, ok := m.find(a, name, keys)
		if !ok || c == nil {
			continue
		}
		if !fn(a, c) {
			return
		}
	}
}

// parseDirective reads the `model.column` out of an `Inherited: …` description.
func (r *Resolver) parseDirective(description string) (string, bool) {
	prefix := r.Cfg.DirectivePrefix
	if !r.Cfg.Directives || prefix == "" {
		return "", false
	}
	trimmed := strings.TrimSpace(description)
	if len(trimmed) <= len(prefix) || !strings.EqualFold(trimmed[:len(prefix)], prefix) {
		return "", false
	}
	target := strings.TrimSpace(trimmed[len(prefix):])
	if target == "" || !strings.Contains(target, ".") {
		return "", false
	}
	return target, true
}

// followDirective resolves a directive to a description and its node.
func (r *Resolver) followDirective(target string) (string, string, string) {
	dot := strings.LastIndexByte(target, '.')
	ref, column := target[:dot], target[dot+1:]
	if ref == "" || column == "" {
		return "", "", "is not `node.column`"
	}

	node, ok := r.Graph.FindNode(ref)
	if !ok {
		return "", "", fmt.Sprintf("names no single node %q", ref)
	}
	c := node.EffectiveColumn(column, r.Cfg.CaseInsensitive)
	if c == nil {
		return "", "", fmt.Sprintf("has no column %q", column)
	}
	if c.Description == "" || r.Cfg.IsPlaceholder(c.Description) {
		return "", "", "points at an undocumented column"
	}
	return c.Description, node.UniqueID, ""
}

func (r *Resolver) resolveNodeLevel(n *dbt.Node, existing Existing, doc *NodeDoc) {
	// A source's own description, when the YAML has none and the node does: only a
	// source provider can report one. Not inheritance, the table describes itself.
	if n.IsSource() && existing.Description == "" && n.Description != "" &&
		!r.Cfg.IsPlaceholder(n.Description) {
		doc.Description, doc.SetDescription = n.Description, true
	}

	if !r.Cfg.InheritNodeDescription {
		return
	}
	local := existing.Description
	if n.Description != "" {
		local = n.Description
	}
	if !r.Cfg.Force && local != "" {
		return
	}
	for _, a := range r.Graph.Ancestors(n.UniqueID) {
		if a.Description != "" && !r.Cfg.IsPlaceholder(a.Description) {
			if a.Description != existing.Description {
				doc.Description, doc.SetDescription, doc.DescriptionFrom = a.Description, true, a.UniqueID
			}
			return
		}
	}
}

// columnTruth is one column of the warehouse-truth column set.
type columnTruth struct {
	name     string
	dataType string
	// comment is the warehouse's own COMMENT, often a source table's only doc.
	comment string
	index   int
}

// trueColumns returns a node's authoritative column list: the catalog if `dbt docs
// generate` ran, else the manifest.
func (r *Resolver) trueColumns(n *dbt.Node) ([]columnTruth, bool) {
	if n.Project != nil && n.Project.Catalog != nil {
		if cn, ok := n.Project.Catalog.Lookup(n); ok {
			cols := cn.Ordered()
			out := make([]columnTruth, 0, len(cols))

			// BigQuery reports both `profile` and `profile.first_name`; DuckDB only `profile`.
			known := make(map[string]bool, len(cols))
			for _, c := range cols {
				known[r.Cfg.Fold(c.Name)] = true
			}

			for _, c := range cols {
				out = append(out, columnTruth{
					name: c.Name, dataType: c.Type, comment: c.Comment, index: c.Index,
				})
				if !r.Cfg.ExpandStructs {
					continue
				}
				for _, f := range dbt.StructFields(c.Name, c.Type) {
					if known[r.Cfg.Fold(f.Path)] {
						continue
					}
					known[r.Cfg.Fold(f.Path)] = true
					out = append(out, columnTruth{name: f.Path, dataType: f.Type, index: c.Index})
				}
			}
			return out, true
		}
	}
	if len(n.Columns) == 0 {
		return nil, false
	}
	out := make([]columnTruth, 0, len(n.Columns))
	for _, c := range n.Columns {
		out = append(out, columnTruth{name: c.Name, dataType: c.DataType})
	}
	// Manifest columns come from an unordered map; sorting keeps runs stable.
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out, false
}

// orderColumns decides the write order.
func (r *Resolver) orderColumns(truth []columnTruth, existing []ExistingColumn, haveTruth bool) []columnTruth {
	switch {
	case r.Cfg.ColumnOrder == config.OrderAlphabetical:
		out := append([]columnTruth(nil), truth...)
		sort.SliceStable(out, func(i, j int) bool { return out[i].name < out[j].name })
		return out
	case r.Cfg.ColumnOrder != config.OrderYAML || !haveTruth:
		return truth
	}

	// YAML order: keep what the file already lists, then append the rest.
	byKey := make(map[string]columnTruth, len(truth))
	for _, t := range truth {
		byKey[r.Cfg.Fold(t.name)] = t
	}
	out := make([]columnTruth, 0, len(truth))
	seen := make(map[string]bool, len(truth))
	for _, e := range existing {
		key := r.Cfg.Fold(e.Name)
		if t, ok := byKey[key]; ok && !seen[key] {
			seen[key] = true
			out = append(out, t)
		}
	}
	for _, t := range truth {
		if key := r.Cfg.Fold(t.name); !seen[key] {
			seen[key] = true
			out = append(out, t)
		}
	}
	return out
}

func entries(m *dbt.OrderedMap) []MetaEntry {
	if m.Len() == 0 {
		return nil
	}
	out := make([]MetaEntry, 0, m.Len())
	for _, k := range m.Keys() {
		v, _ := m.Get(k)
		out = append(out, MetaEntry{Key: k, Value: v})
	}
	return out
}

// definitiveFor returns the settled description for a column name, if declared.
func (r *Resolver) definitiveFor(name string) (Definitive, bool) {
	d, ok := r.Definitives[r.Cfg.Fold(name)]
	return d, ok
}

// declaresDefinitive reports whether this node's column carries the marker,
// making it the decision rather than a recipient.
func (r *Resolver) declaresDefinitive(n *dbt.Node, name string) bool {
	c := n.Column(name, r.Cfg.CaseInsensitive)
	return c != nil && isDefinitive(c)
}

func (r *Resolver) applyCase(s string) string {
	switch r.Cfg.ColumnCase {
	case "lower":
		return strings.ToLower(s)
	case "upper":
		return strings.ToUpper(s)
	default:
		return s
	}
}
