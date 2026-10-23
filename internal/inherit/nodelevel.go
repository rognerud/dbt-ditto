package inherit

import (
	"reflect"
	"slices"

	"github.com/rognerud/dbt-ditto/internal/dbt"
)

// Table-level meta and tags, from the three places a table is described: the
// schema YAML and manifest entry it has of its own, the ancestors it is built
// from, and the `+meta:` / `+tags:` its folders declare in dbt_project.yml.
//
// The origins are weakest-last. What a table says about itself is never
// replaced, because somebody wrote it; then the nearest ancestor that says
// anything about a key; then dbt_project.yml, which is a default for everything
// below it and so the weakest claim of the three. `inheritance.force` does not
// enter into it: forcing applies to a description somebody wrote, and a meta
// key is not a description — a forced run that replaced an owner with an
// upstream owner would be destroying the one thing node meta is used for.
//
// Meta merges key by key, so a table naming its own owner still inherits the
// tier it says nothing about. Tags are an order-preserving union with the local
// ones first, exactly as a column's are.

// resolveNodeMetaAndTags fills in NodeDoc.Meta and NodeDoc.Tags.
func (r *Resolver) resolveNodeMetaAndTags(n *dbt.Node, existing Existing, doc *NodeDoc) {
	if r.Cfg.NodeMeta {
		if meta, changed := r.nodeMeta(n, existing); changed {
			doc.Meta, doc.SetMeta = meta, true
		}
	}
	if r.Cfg.NodeTags {
		if tags, changed := r.nodeTags(n, existing); changed {
			doc.Tags, doc.SetTags = tags, true
		}
	}
}

// nodeMeta returns the node's complete meta, and whether it differs from what
// the YAML already has. Nothing is written when it does not: a run that
// rewrote every entry it merely looked at would defeat `--check`.
func (r *Resolver) nodeMeta(n *dbt.Node, existing Existing) ([]MetaEntry, bool) {
	out := append([]MetaEntry(nil), existing.Meta...)
	have := make(map[string]bool, len(out))
	for _, e := range out {
		have[e.Key] = true
	}
	// The manifest entry is the table describing itself too: a `meta:` written
	// in a schema file this run is not editing still belongs to the table.
	add := func(meta map[string]any) {
		for _, k := range dbt.SortedKeys(meta) {
			if have[k] {
				continue
			}
			have[k] = true
			out = append(out, MetaEntry{Key: k, Value: meta[k]})
		}
	}
	add(n.EffectiveMeta())

	for _, a := range r.nodeAncestors(n) {
		add(a.EffectiveMeta())
	}
	if r.Cfg.NodeFromFolders && n.Project != nil {
		add(n.Project.FolderMeta(n))
	}

	return out, !sameMeta(existing.Meta, out)
}

// nodeTags returns the node's complete tag list, and whether it differs from
// what the YAML already has.
func (r *Resolver) nodeTags(n *dbt.Node, existing Existing) ([]string, bool) {
	out := dbt.UnionTags(append([]string(nil), existing.Tags...), n.EffectiveTags())
	for _, a := range r.nodeAncestors(n) {
		out = dbt.UnionTags(out, a.EffectiveTags())
	}
	if r.Cfg.NodeFromFolders && n.Project != nil {
		out = dbt.UnionTags(out, n.Project.FolderTags(n))
	}
	return out, !slices.Equal(existing.Tags, out)
}

// nodeAncestors is the ancestors node-level inheritance may read, nearest
// first. A source is an ancestor like any other, so from_sources decides only
// whether that ancestor counts.
func (r *Resolver) nodeAncestors(n *dbt.Node) []*dbt.Node {
	if !r.Cfg.NodeFromParents && !r.Cfg.NodeFromSources {
		return nil
	}
	all := r.Graph.Ancestors(n.UniqueID)
	out := make([]*dbt.Node, 0, len(all))
	for _, a := range all {
		if a.IsSource() {
			if r.Cfg.NodeFromSources {
				out = append(out, a)
			}
			continue
		}
		if r.Cfg.NodeFromParents {
			out = append(out, a)
		}
	}
	return out
}

// sameMeta compares two meta lists by key order and value, so a run that
// resolved exactly what the file already says writes nothing.
func sameMeta(a, b []MetaEntry) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		// A meta value is whatever the YAML held — a scalar, a list, a nested
		// mapping — so the comparison has to be structural.
		if a[i].Key != b[i].Key || !reflect.DeepEqual(a[i].Value, b[i].Value) {
			return false
		}
	}
	return true
}
