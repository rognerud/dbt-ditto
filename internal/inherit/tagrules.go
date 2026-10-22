package inherit

import (
	"slices"
	"strings"

	"github.com/rognerud/dbt-ditto/internal/config"
	"github.com/rognerud/dbt-ditto/internal/dbt"
)

// LabelDefaultsKey (`meta: {dbt_ditto_label_defaults: [data-sensitivity]}`) names
// the label keys this tool decided for a column, as opposed to the ones somebody
// wrote. It is what lets a default be revised — downgraded as well as raised —
// while an explicit value is never touched again: a key present on the column
// but absent from this list is canonical.
const LabelDefaultsKey = "dbt_ditto_label_defaults"

// WarnLabelUnknown: a value outside the rule's vocabulary. It takes part in
// nothing, because its strength is undefined, and it is left where it was
// written rather than quietly dropped.
const WarnLabelUnknown = "label_unknown"

// localLabels is the column's own label values before any ancestor was folded
// in, with the ones a previous run decided marked as such.
type localLabels struct {
	values   map[string]string
	defaults map[string]bool
}

// explicit returns the value a person put on this column, which outranks
// anything upstream. A value a previous run wrote as a default is not explicit,
// however exactly it looks like one.
func (l localLabels) explicit(key string) (string, bool) {
	if l.defaults[key] {
		return "", false
	}
	v, ok := l.values[key]
	return v, ok && v != ""
}

// snapshotLabels reads the column's own labels out of the seeded knowledge,
// before inheritance overwrites them.
func (r *Resolver) snapshotLabels(k *knowledge) localLabels {
	out := localLabels{values: map[string]string{}, defaults: map[string]bool{}}
	if len(r.Cfg.TagRules) == 0 {
		return out
	}
	for _, key := range labelDefaults(k.meta) {
		out.defaults[key] = true
	}
	for _, rule := range r.Cfg.TagRules {
		if v, ok := r.labelFrom(k.meta, k.tags, rule.Key); ok {
			out.values[rule.Key] = v
		}
	}
	return out
}

// labelDefaults reads the marker list off a column's meta.
func labelDefaults(m *dbt.OrderedMap) []string {
	v, ok := m.Get(LabelDefaultsKey)
	if !ok {
		return nil
	}
	switch t := v.(type) {
	case []string:
		return t
	case []any:
		out := make([]string, 0, len(t))
		for _, e := range t {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	case string:
		// A single key, as somebody editing the file by hand would write it.
		return []string{t}
	default:
		return nil
	}
}

// applyTagRules collapses every rule's key to one value for this column, and is
// where a folder's tags and an explicit value get their precedence over
// anything inherited. It runs after inheritance, on the union it produced.
func (r *Resolver) applyTagRules(k *knowledge, n *dbt.Node, generations [][]*dbt.Node,
	name string, keys [matchNone][]string, local localLabels) {

	folder := r.folderTags(n)
	if r.Cfg.FolderTags == config.FolderTagsAll {
		// Every folder tag, not only the ruled ones: the folder is the statement.
		k.tags = dbt.UnionTags(k.tags, folder)
	}
	if len(r.Cfg.TagRules) == 0 {
		return
	}

	candidates := r.ruleCandidates(n, generations, name, keys)
	var decided []string
	for _, rule := range r.Cfg.TagRules {
		chosen, isDefault := r.chooseLabel(k, rule, folder, local, candidates[rule.Key], name)
		if chosen == "" {
			// A default whose basis is gone goes with it: the upstream value that
			// justified it has been removed, and keeping it would be this tool
			// asserting something nothing upstream says any more.
			if local.defaults[rule.Key] {
				r.stripLabel(k, rule)
			}
			continue
		}
		r.stripLabel(k, rule)
		r.writeLabel(k, rule, chosen)
		if isDefault {
			decided = append(decided, rule.Key)
		}
	}

	// Rewritten whole each run: a key that stopped being a default must stop being
	// listed, or the next run would treat an explicit value as its own.
	if len(decided) > 0 {
		k.meta.Set(LabelDefaultsKey, decided)
	} else {
		k.meta.Delete(LabelDefaultsKey)
	}
}

// chooseLabel returns the value a column should end up with for one rule, and
// whether that value is a default this tool may revise later.
func (r *Resolver) chooseLabel(k *knowledge, rule config.ResolvedTagRule, folder []string,
	local localLabels, candidates []labelCandidate, column string) (string, bool) {

	// A folder's declaration is canonical for every node below it, and is checked
	// first: it is the coarsest statement anybody made on purpose, and the one an
	// upstream value must not undo.
	if r.Cfg.FolderTagsRead() {
		if v, ok := r.labelFromTags(folder, rule.Key); ok {
			r.noteUnknown(k, rule, v, column, "the folder it is in")
			return v, false
		}
	}
	if v, ok := local.explicit(rule.Key); ok {
		r.noteUnknown(k, rule, v, column, "this column")
		return v, false
	}

	best, bestRank, bestDepth, bestNode := "", 0, 0, ""
	for _, c := range candidates {
		rank, known := rule.Rank(c.value)
		if !known {
			r.noteUnknown(k, rule, c.value, column, c.node)
			continue
		}
		switch {
		case best == "":
		case rule.Resolve == config.ResolveFirst:
			// Nearest wins, as a description does. Two branches can reach the same
			// depth, so the unique_id breaks the tie — the same tie-break, and so the
			// same answer whichever order the manifest listed the nodes in.
			if c.depth > bestDepth || (c.depth == bestDepth && c.node >= bestNode) {
				continue
			}
		case rule.Resolve == config.ResolveWeakest && rank >= bestRank:
			continue
		case rule.Resolve != config.ResolveWeakest && rank <= bestRank:
			continue
		}
		best, bestRank, bestDepth, bestNode = c.value, rank, c.depth, c.node
	}
	if best != "" {
		return best, true
	}
	return rule.Default, rule.Default != ""
}

// noteUnknown reports a value the rule's vocabulary does not list.
func (r *Resolver) noteUnknown(k *knowledge, rule config.ResolvedTagRule, value, column, where string) {
	if _, known := rule.Rank(value); known {
		return
	}
	k.note(column, WarnLabelUnknown,
		"%s=%q on %s is not one of %v, so its strength is undefined and it took part in no comparison",
		rule.Key, value, where, rule.Values)
}

// labelCandidate is one ancestor's value for a ruled key, with how many hops up
// it was found — which is what `resolve: first` compares.
type labelCandidate struct {
	value string
	node  string
	depth int
}

// ruleCandidates collects what the ancestors say about each ruled key. Unlike a
// description, which the first ancestor of a generation claims, a rule needs a
// set: picking the strongest is only meaningful across the whole of it.
func (r *Resolver) ruleCandidates(n *dbt.Node, generations [][]*dbt.Node, name string,
	keys [matchNone][]string) map[string][]labelCandidate {

	out := make(map[string][]labelCandidate, len(r.Cfg.TagRules))
	for _, rule := range r.Cfg.TagRules {
		if rule.Cuts() {
			out[rule.Key] = r.cutCandidates(n, rule, name, keys)
			continue
		}
		out[rule.Key] = r.everyAncestorsValue(generations, rule, name, keys)
	}
	return out
}

// everyAncestorsValue is `inherit: all`: every ancestor of every generation has
// a say, so nothing below a classified table can be written weaker than it —
// including by writing a weaker value halfway down.
func (r *Resolver) everyAncestorsValue(generations [][]*dbt.Node, rule config.ResolvedTagRule,
	name string, keys [matchNone][]string) []labelCandidate {

	var out []labelCandidate
	for depth, generation := range generations {
		for _, a := range generation {
			if value, _ := r.declaredLabel(a, rule, name, keys); value != "" {
				out = append(out, labelCandidate{value: value, node: a.UniqueID, depth: depth + 1})
			}
		}
	}
	return out
}

// cutCandidates is `inherit: cut`, and the reason a column altered halfway down
// the DAG keeps its own value: each branch is followed upwards only until an
// ancestor that *declares* the key is reached. What fed that ancestor was the
// input to its decision, not a second opinion about it.
//
// A branch that reaches a root without a declaration contributes whatever value
// it carries, so a classification on a raw table still travels the whole way
// down when nobody has altered it on the way.
func (r *Resolver) cutCandidates(n *dbt.Node, rule config.ResolvedTagRule,
	name string, keys [matchNone][]string) []labelCandidate {

	// Depths are relative to the node asked about, so a memoised answer can be
	// reused one hop lower by adding one — which is what keeps `resolve: first`
	// honest where two branches rejoin.
	memo := map[string][]labelCandidate{}
	open := map[string]bool{}

	var from func(string) []labelCandidate
	from = func(id string) []labelCandidate {
		if cached, ok := memo[id]; ok {
			return cached
		}
		// A node already on the stack is a cycle, which dbt should not produce but
		// a hand-written manifest can. Each node is entered once, so this walk
		// terminates on any graph without needing a depth bound.
		if open[id] {
			return nil
		}
		open[id] = true

		var out []labelCandidate
		for _, pid := range r.Graph.Parents(id) {
			p, ok := r.Graph.Nodes[pid]
			if !ok {
				continue
			}
			value, declared := r.declaredLabel(p, rule, name, keys)
			if declared {
				out = append(out, labelCandidate{value: value, node: pid, depth: 1})
				continue
			}
			above := from(pid)
			if len(above) == 0 {
				// Nothing above has an opinion, so the value this node carries — a
				// default an earlier run left, or nothing — is all there is.
				if value != "" {
					out = append(out, labelCandidate{value: value, node: pid, depth: 1})
				}
				continue
			}
			for _, c := range above {
				c.depth++
				out = append(out, c)
			}
		}

		delete(open, id)
		memo[id] = out
		return out
	}
	return from(n.UniqueID)
}

// declaredLabel reads an ancestor's value for a ruled key, and reports whether
// that ancestor *declared* it rather than inherited it.
//
// The distinction is the `dbt_ditto_label_defaults` marker, which is written
// into the schema file and so comes back through dbt's next parse: a value
// listed there is one of this tool's own derivations, and carries no decision to
// respect. A folder's `+tags:` is always a declaration, since a folder is
// something somebody wrote on purpose.
func (r *Resolver) declaredLabel(a *dbt.Node, rule config.ResolvedTagRule,
	name string, keys [matchNone][]string) (string, bool) {

	if r.Cfg.FolderTagsRead() {
		if v, ok := r.labelFromTags(r.folderTags(a), rule.Key); ok {
			return v, true
		}
	}
	c, _, found := r.matcher().find(a, name, keys)
	if !found || c == nil {
		return "", false
	}
	meta := c.EffectiveMeta()
	v, ok := r.labelFrom(meta, c.EffectiveTags(), rule.Key)
	if !ok {
		return "", false
	}
	return v, !slices.Contains(labelDefaults(meta), rule.Key)
}

// folderTags returns the `+tags:` the node's folders declare in dbt_project.yml.
func (r *Resolver) folderTags(n *dbt.Node) []string {
	if !r.Cfg.FolderTagsRead() || n == nil || n.Project == nil {
		return nil
	}
	return n.Project.FolderTags(n)
}

// labelFrom reads a label key off a column, preferring meta — the structured
// route — over the rendered tag.
func (r *Resolver) labelFrom(meta *dbt.OrderedMap, tags []string, key string) (string, bool) {
	if v, ok := r.labelFromMeta(meta, key); ok {
		return v, true
	}
	return r.labelFromTags(tags, key)
}

// labelFromMeta reads the key out of the nested label map, or out of meta itself
// when labels are configured flat.
func (r *Resolver) labelFromMeta(meta *dbt.OrderedMap, key string) (string, bool) {
	if meta == nil {
		return "", false
	}
	holder := meta
	if nested := r.labelKey(); nested != "" {
		v, ok := meta.Get(nested)
		if !ok {
			return "", false
		}
		m, ok := v.(*dbt.OrderedMap)
		if !ok {
			return "", false
		}
		holder = m
	}
	v, ok := holder.Get(key)
	if !ok {
		return "", false
	}
	s, ok := v.(string)
	return s, ok && s != ""
}

// labelFromTags finds the first tag rendering the key, in the configured format.
func (r *Resolver) labelFromTags(tags []string, key string) (string, bool) {
	t := r.tagTemplate()
	for _, tag := range tags {
		if k, v, ok := t.parse(tag); ok && k == key && v != "" {
			return v, true
		}
	}
	return "", false
}

// stripLabel removes every recognised rendering of the key, so the value about
// to be written is the only one left. A value outside the vocabulary is left
// alone: it was reported, and deleting what somebody wrote is worse than
// leaving it next to the resolved one.
func (r *Resolver) stripLabel(k *knowledge, rule config.ResolvedTagRule) {
	t := r.tagTemplate()
	kept := make([]string, 0, len(k.tags))
	for _, tag := range k.tags {
		key, value, ok := t.parse(tag)
		if ok && key == rule.Key {
			if _, known := rule.Rank(value); known {
				continue
			}
		}
		kept = append(kept, tag)
	}
	k.tags = kept

	if nested := r.labelKey(); nested != "" {
		if m, ok := r.nestedLabels(k, false); ok {
			m.Delete(rule.Key)
			if m.Len() == 0 {
				k.meta.Delete(nested)
			}
		}
		return
	}
	k.meta.Delete(rule.Key)
}

// writeLabel puts the resolved value wherever the rule routes it.
func (r *Resolver) writeLabel(k *knowledge, rule config.ResolvedTagRule, value string) {
	if rule.ToTags() {
		k.tags = dbt.UnionTags(k.tags, []string{r.tagTemplate().render(rule.Key, value)})
	}
	if !rule.ToMeta() {
		return
	}
	if r.labelKey() == "" {
		k.meta.Set(rule.Key, value)
		return
	}
	m, _ := r.nestedLabels(k, true)
	m.Set(rule.Key, value)
}

// nestedLabels returns the label map under the meta key, cloning it before it is
// handed out: knowledge.meta is a shallow copy of the manifest column's, so
// writing through the value found there would edit the ancestor everybody else
// is about to read.
func (r *Resolver) nestedLabels(k *knowledge, create bool) (*dbt.OrderedMap, bool) {
	nested := r.labelKey()
	if nested == "" {
		return nil, false
	}
	if v, ok := k.meta.Get(nested); ok {
		if m, ok := v.(*dbt.OrderedMap); ok {
			clone := m.Clone()
			k.meta.Set(nested, clone)
			return clone, true
		}
	}
	if !create {
		return nil, false
	}
	m := dbt.NewOrderedMap()
	k.meta.Set(nested, m)
	return m, true
}

// tagTemplate caches the parse of `sources.labels.tag_format`.
func (r *Resolver) tagTemplate() tagTemplate {
	r.tagOnce.Do(func() { r.tagTmpl = newTagTemplate(r.Cfg.SourceLabels.TagFormat) })
	return r.tagTmpl
}

// tagTemplate renders a label pair as a tag and reads one back, from the single
// `{key}`/`{value}` format the configuration already carries.
type tagTemplate struct {
	prefix, mid, suffix string
	// parsable is false for a format a tag cannot be read back out of, such as one
	// with no separator between the two fields.
	parsable bool
}

func newTagTemplate(format string) tagTemplate {
	ki, vi := strings.Index(format, "{key}"), strings.Index(format, "{value}")
	if ki < 0 || vi < 0 || vi < ki {
		return tagTemplate{}
	}
	t := tagTemplate{
		prefix:   format[:ki],
		mid:      format[ki+len("{key}") : vi],
		suffix:   format[vi+len("{value}"):],
		parsable: true,
	}
	// Without a separator `a:b` could be read as any split of the two fields.
	if t.mid == "" {
		t.parsable = false
	}
	return t
}

func (t tagTemplate) render(key, value string) string {
	if !t.parsable {
		return key + ":" + value
	}
	return t.prefix + key + t.mid + value + t.suffix
}

func (t tagTemplate) parse(tag string) (string, string, bool) {
	if !t.parsable {
		return "", "", false
	}
	rest, ok := cut(tag, t.prefix, true)
	if !ok {
		return "", "", false
	}
	rest, ok = cut(rest, t.suffix, false)
	if !ok {
		return "", "", false
	}
	key, value, found := strings.Cut(rest, t.mid)
	if !found || key == "" {
		return "", "", false
	}
	return key, value, true
}

// cut trims a fixed prefix or suffix, reporting whether it was there.
func cut(s, fix string, prefix bool) (string, bool) {
	if fix == "" {
		return s, true
	}
	if prefix {
		return strings.TrimPrefix(s, fix), strings.HasPrefix(s, fix)
	}
	return strings.TrimSuffix(s, fix), strings.HasSuffix(s, fix)
}

// ruledKeys is the set of label keys a rule decides, which the generic
// disagreement reporting leaves alone: a rule is the answer to that
// disagreement, so warning about it would ask a question already settled.
func (r *Resolver) ruledKeys() map[string]bool {
	if len(r.Cfg.TagRules) == 0 {
		return nil
	}
	out := make(map[string]bool, len(r.Cfg.TagRules))
	for _, rule := range r.Cfg.TagRules {
		out[rule.Key] = true
	}
	return out
}
