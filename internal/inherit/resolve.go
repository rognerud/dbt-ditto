package inherit

import (
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/rognerud/dbt-ditto/internal/config"
	"github.com/rognerud/dbt-ditto/internal/dbt"
)

// MetaEntry is one key of a column's `meta`, kept in a slice rather than a map
// so the order the analyst wrote survives a round trip.
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

	// Description is the final description. SetDescription is false when the
	// column should keep whatever it already has.
	Description    string
	SetDescription bool

	DataType    string
	SetDataType bool

	// Meta and Tags are the complete desired sets, in write order.
	Meta []MetaEntry
	Tags []string
	// Extra are the column keys this tool does not model but was asked to carry
	// (`inheritance.extra_keys`), in the order they were configured.
	Extra []MetaEntry

	// Progenitor is the unique_id the description came from, empty when the
	// description was already local.
	Progenitor string
}

// NodeDoc is the resolved documentation for one node.
type NodeDoc struct {
	Node *dbt.Node

	Description     string
	SetDescription  bool
	DescriptionFrom string

	Meta []MetaEntry

	// Columns is the full desired column list, in write order.
	Columns []ColumnDoc
	// Drop lists YAML column names that no longer exist in the warehouse.
	Drop []string
	// Warnings are things worth telling the analyst that are not errors: a
	// directive that points nowhere, or a column several parents disagree about.
	Warnings []Warning
}

// Warning kinds.
const (
	// WarnAmbiguous: several parents document the column differently, so which
	// description wins is decided by unique_id order and is arbitrary.
	WarnAmbiguous = "ambiguous"
	// WarnDirective: an `Inherited: model.column` pointer could not be resolved.
	WarnDirective = "directive"
	// WarnShadowedSource: a `source:` points at a relation a loaded project
	// builds. Declared as a cross-project ref it would inherit documentation
	// through the graph; declared as a source it is a root, and inherits
	// nothing.
	WarnShadowedSource = "shadowed_source"
)

// Warning is one thing the run wants to say about a column without failing.
type Warning struct {
	Node   string
	Column string
	Kind   string
	Detail string
}

// Changed reports whether the resolution asks for any edit at all.
func (d *NodeDoc) Changed() bool {
	if d.SetDescription || len(d.Meta) > 0 || len(d.Drop) > 0 {
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

	// Definitives are the settled descriptions, keyed by folded column name.
	// Build them with BuildDefinitives, which also reports conflicts.
	Definitives map[string]Definitive

	matchOnce sync.Once
	match     *matcher
}

// matcher returns the column matcher, built on first use so a zero Resolver
// still works.
func (r *Resolver) matcher() *matcher {
	r.matchOnce.Do(func() { r.match = newMatcher(r.Cfg) })
	return r.match
}

// ExistingColumn is the state of a column as currently written in YAML.
type ExistingColumn struct {
	Name        string
	Description string
	Meta        []MetaEntry
	Tags        []string
}

// Existing is the current YAML state of a node, read from the schema file.
type Existing struct {
	Present     bool
	Description string
	Meta        []MetaEntry
	Columns     []ExistingColumn
}

// knowledge is the accumulated view of one column while walking its ancestors,
// mirroring dbt-osmosis' "column knowledge graph" entry.
type knowledge struct {
	description string
	meta        *dbt.OrderedMap
	tags        []string
	// extra holds the values of `inheritance.extra_keys`, which travel exactly
	// as meta does: an ancestor's value overwrites what the column had.
	extra map[string]any
	// notes are warnings raised while folding ancestors in — a label that was
	// not carried across, or one two ancestors disagree about. They are
	// collected here because inheritInto walks the generations and Resolve owns
	// the node's warning list.
	notes []Warning
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
		existingByKey[r.fold(existing.Columns[i].Name)] = &existing.Columns[i]
	}

	// truth is the column list to write. haveTruth says whether it came from the
	// warehouse, which is the only thing that licenses deleting a column.
	truth, haveTruth := r.trueColumns(n)
	if len(truth) == 0 {
		// Nothing known about the relation at all: enrich what the file already
		// lists and add nothing.
		for _, c := range existing.Columns {
			truth = append(truth, columnTruth{name: c.Name})
		}
	}

	truthByKey := make(map[string]bool, len(truth))
	for _, t := range truth {
		truthByKey[r.fold(t.name)] = true
	}

	generations := r.Graph.Generations(n.UniqueID)

	for _, t := range r.orderColumns(truth, existing.Columns, haveTruth) {
		key := r.fold(t.name)
		ex := existingByKey[key]
		if ex == nil && !r.Cfg.AddMissing {
			continue
		}

		cd := ColumnDoc{Name: t.name, Existing: ex != nil}
		if ex != nil {
			cd.Name = ex.Name // never churn an already-written spelling
		} else {
			cd.Name = r.applyCase(t.name)
		}

		k := r.seedKnowledge(n, t.name, ex)
		if k.description == "" && t.comment != "" && r.useComment(n, t.name, ex) {
			// The warehouse's own comment counts as the column's documentation,
			// so inheritance treats it exactly like a description written by
			// hand: it is kept, and nothing upstream overwrites it.
			k.description = t.comment
		}
		local := k.description

		// A directive is not a description, it is an instruction about where the
		// description lives. Resolve it before anything else looks at the text.
		directive, hasDirective := parseDirective(local, r.Cfg.DirectivePrefix, r.Cfg.Directives)
		var directiveDesc, directiveFrom string
		if hasDirective {
			var err string
			directiveDesc, directiveFrom, err = r.followDirective(directive)
			if err != "" {
				doc.Warnings = append(doc.Warnings, Warning{
					Node: n.UniqueID, Column: t.name, Kind: WarnDirective,
					Detail: fmt.Sprintf("%q %s", directive, err),
				})
			}
		}

		progenitor, competing := r.inheritInto(&k, generations, t.name)
		for _, w := range k.notes {
			w.Node = n.UniqueID
			doc.Warnings = append(doc.Warnings, w)
		}
		definitive, settled := r.definitiveFor(t.name)
		if settled {
			// The disagreement has been decided in writing, so it is no longer
			// something to tell the analyst about.
			competing = nil
		}
		// competing is also computed for the meta annotation below, so the
		// warning stays gated on its own switch.
		if len(competing) > 0 && r.Cfg.WarnAmbiguous {
			doc.Warnings = append(doc.Warnings, Warning{
				Node: n.UniqueID, Column: t.name, Kind: WarnAmbiguous,
				Detail: fmt.Sprintf("documented differently by %s; took %s",
					strings.Join(competing, ", "), progenitor),
			})
		}

		if k.description == "" && r.backfills(n) {
			// Still nothing: look downstream. Only an empty description is
			// filled this way, so backfill can never overwrite anything.
			if desc, from, ok := r.backfill(n, t.name); ok {
				k.description, progenitor = desc, from
			}
		}

		// A column keeps its own description whenever it has one; only an
		// empty description inherits. The description written back is the
		// manifest's, so a `{{ doc(...) }}` reference lands rendered, which is
		// what dbt-osmosis writes.
		final := k.description
		if !r.Cfg.Force && local != "" {
			final, progenitor = local, ""
		}
		// A resolved directive outranks everything, including `force`: it is the
		// analyst naming the column to copy from, which no rule should override.
		if directiveDesc != "" {
			final, progenitor = directiveDesc, directiveFrom
		}

		// A definitive declaration outranks even that. The column that carries
		// the marker is the decision itself, so it keeps its own wording and
		// inherits nothing; every other column of that name takes the settled
		// wording, whether it is upstream, downstream or in another project.
		// Nothing about it is ambiguous any more, so the disagreement that
		// prompted the declaration stops being reported.
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
			// Annotate only a description that was actually inherited while the
			// parents disagreed. Settling it locally or with a directive clears
			// `progenitor`, and the stale annotation is deleted rather than left
			// behind claiming a disagreement that no longer decides anything.
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
			if !truthByKey[r.fold(c.Name)] {
				doc.Drop = append(doc.Drop, c.Name)
			}
		}
	}
	return doc
}

// backfills reports whether a node may take documentation from downstream.
func (r *Resolver) backfills(n *dbt.Node) bool {
	if !r.Cfg.Backfill {
		return false
	}
	return !r.Cfg.BackfillSourcesOnly || n.IsSource()
}

// backfill finds a description for a column among the node's descendants,
// nearest generation first, and returns the node it came from. It stops at the
// first real description, not the first descendant that has the column: a
// staging model usually selects a source column through undocumented.
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

// useComment reports whether a warehouse comment may be used as a column's
// description. dbt-osmosis reads it only when it invents the column entry, so
// an already-written column keeps whatever the YAML says, even nothing; that is
// the default. `columns.comments: always` also fills an existing but
// undocumented column.
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

// seedKnowledge starts a column's knowledge entry from what the node itself
// knows. The manifest is preferred over the YAML because dbt has already
// rendered `{{ doc(...) }}` references there, which is the text dbt-osmosis
// writes back.
func (r *Resolver) seedKnowledge(n *dbt.Node, name string, ex *ExistingColumn) knowledge {
	k := knowledge{meta: dbt.NewOrderedMap()}
	if mc := n.Column(name, r.Cfg.CaseInsensitive); mc != nil {
		k.description = mc.Description
		if m := mc.EffectiveMeta(); m != nil {
			k.meta = m.Clone()
		}
		k.tags = append(k.tags, mc.EffectiveTags()...)
		k.extra = copyExtra(mc.Extra, r.Cfg.ExtraKeys)
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

// inheritInto folds every ancestor generation into k, furthest first, and
// returns the unique_id the surviving description came from.
//
// The second return value names ancestors in the winner's own generation that
// documented the column differently. Within a generation the first ancestor by
// unique_id claims it, so that disagreement is settled alphabetically — which
// is arbitrary, and worth saying out loud. A nearer generation overriding a
// further one is inheritance working, and is not reported.
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
			// First ancestor in this generation with the column claims it; the
			// rest of the generation is skipped, as dbt-osmosis does.
			if r.Cfg.InheritTags {
				k.tags = dbt.UnionTags(k.tags, c.EffectiveTags())
			}
			for _, ek := range r.Cfg.ExtraKeys {
				v, ok := c.Extra[ek]
				if !ok || v == nil {
					continue
				}
				if k.extra == nil {
					k.extra = map[string]any{}
				}
				k.extra[ek] = v
			}
			if r.Cfg.InheritMeta {
				labelKey := r.labelKey()
				for _, mk := range c.EffectiveMeta().Keys() {
					// An ancestor's own annotations describe that ancestor's column,
					// not this one: the progenitor is recomputed below, an upstream
					// disagreement was settled upstream, and the definitive marker
					// names the one column that *is* the decision.
					if r.Cfg.SkipMetaKeys[mk] || mk == r.Cfg.ProgenitorKey ||
						mk == r.Cfg.AmbiguityKey || mk == DefinitiveKey {
						continue
					}
					v, _ := c.EffectiveMeta().Get(mk)
					if labelKey != "" && mk == labelKey {
						// Labels travel under their own rules: they say what may be
						// done with the data, not what it means.
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
				// Carry the original progenitor forward rather than pointing at
				// the intermediate model that also inherited the description.
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

// dissenters names the ancestors in the rest of a generation that document the
// column, but not the way the winner does.
func (r *Resolver) dissenters(rest []*dbt.Node, name string, keys [matchNone][]string, won string) []string {
	if !r.Cfg.WarnAmbiguous && !r.Cfg.AmbiguityMeta {
		return nil
	}
	m := r.matcher()
	var out []string
	for _, a := range rest {
		c, _, ok := m.find(a, name, keys)
		if !ok || c == nil {
			continue
		}
		if c.Description == "" || r.Cfg.IsPlaceholder(c.Description) || c.Description == won {
			continue
		}
		out = append(out, a.UniqueID)
	}
	return out
}

// parseDirective reads `Inherited: model.column` out of a description. The node
// reference may be a bare name or a full unique_id; the column is whatever
// follows the last dot, so a struct field has to be written with the node as a
// unique_id to stay unambiguous.
func parseDirective(description, prefix string, enabled bool) (string, bool) {
	if !enabled || prefix == "" {
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

// followDirective resolves a directive to a description and the node it came
// from. The third return value is a human-readable reason when it cannot be
// resolved, in which case the directive text is left in the file: silently
// dropping it would lose the analyst's instruction, and silently keeping it
// without a word would look like documentation.
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
	// A source's own description, when the YAML has none and the node does.
	// For every other node the two agree, since the node's description came from
	// that YAML — so this fires only for a source provider reporting what the
	// warehouse says the table is. Not inheritance: the table describes itself.
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
	// comment is the warehouse's own description of the column: a Snowflake or
	// BigQuery COMMENT, say. For a source table nothing upstream can supply a
	// description, so this is often the only documentation that exists.
	comment string
	index   int
}

// trueColumns returns the authoritative column list for a node: the catalog if
// `dbt docs generate` has been run, otherwise whatever the manifest knows. The
// second return value reports whether the list really is warehouse truth, which
// is what licenses removing columns.
func (r *Resolver) trueColumns(n *dbt.Node) ([]columnTruth, bool) {
	if n.Project != nil && n.Project.Catalog != nil {
		if cn, ok := n.Project.Catalog.Lookup(n); ok {
			cols := cn.Ordered()
			out := make([]columnTruth, 0, len(cols))

			// BigQuery's catalog joins through COLUMN_FIELD_PATHS, so both
			// `profile` and `profile.first_name` arrive as columns; DuckDB
			// reports only `profile`, with the fields inside its composite type.
			// Expanding blindly would duplicate every BigQuery field, so anything
			// the catalog already names is left alone.
			known := make(map[string]bool, len(cols))
			for _, c := range cols {
				known[r.fold(c.Name)] = true
			}

			for _, c := range cols {
				out = append(out, columnTruth{
					name: c.Name, dataType: c.Type, comment: c.Comment, index: c.Index,
				})
				if !r.Cfg.ExpandStructs {
					continue
				}
				for _, f := range dbt.StructFields(c.Name, c.Type) {
					if known[r.fold(f.Path)] {
						continue
					}
					known[r.fold(f.Path)] = true
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
		byKey[r.fold(t.name)] = t
	}
	out := make([]columnTruth, 0, len(truth))
	seen := make(map[string]bool, len(truth))
	for _, e := range existing {
		key := r.fold(e.Name)
		if t, ok := byKey[key]; ok && !seen[key] {
			seen[key] = true
			out = append(out, t)
		}
	}
	for _, t := range truth {
		if key := r.fold(t.name); !seen[key] {
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

// definitiveFor returns the settled description for a column name, if one has
// been declared anywhere in the graph.
func (r *Resolver) definitiveFor(name string) (Definitive, bool) {
	if len(r.Definitives) == 0 {
		return Definitive{}, false
	}
	d, ok := r.Definitives[r.fold(name)]
	return d, ok
}

// declaresDefinitive reports whether this node's own column carries the marker,
// which is what makes it the source of the decision rather than a recipient.
func (r *Resolver) declaresDefinitive(n *dbt.Node, name string) bool {
	c := n.Column(name, r.Cfg.CaseInsensitive)
	return c != nil && isDefinitive(c)
}

func (r *Resolver) fold(s string) string {
	if r.Cfg.CaseInsensitive {
		return strings.ToLower(s)
	}
	return s
}

// Fold exposes the column-name folding this resolver matches with, so
// definitives are keyed exactly the way lookups will read them.
func (r *Resolver) Fold(s string) string { return r.fold(s) }

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
