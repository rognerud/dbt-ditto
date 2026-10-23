package dbt

import "sort"

// A node's meta and tags are declared in two places, exactly as a column's are:
// at the top level of the YAML entry, and inside its `config:` block. dbt reads
// both and merges them, with `config:` winning, so these do too.
//
// Unlike a column's, a node's arrive as a plain map: `Node.Meta` is
// `map[string]any` because `manifest.json` gives no order to a node's meta that
// dbt itself preserves. Keys therefore come back sorted, which is the only
// stable order available and keeps a rewrite from reordering a file it did not
// otherwise change.

// EffectiveMeta is the node's own meta merged with its `config.meta`.
func (n *Node) EffectiveMeta() map[string]any {
	if n == nil {
		return nil
	}
	out := make(map[string]any, len(n.Meta))
	for k, v := range n.Meta {
		out[k] = v
	}
	if cfg, ok := n.Config["meta"].(map[string]any); ok {
		for k, v := range cfg {
			out[k] = v
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// EffectiveTags is the union of the node's tags and its `config.tags`, the
// node's own first.
func (n *Node) EffectiveTags() []string {
	if n == nil {
		return nil
	}
	return UnionTags(n.Tags, decodeAnyStrings(n.Config["tags"]))
}

// SortedKeys is the order a map's keys are read in, so two runs over the same
// manifest write the same bytes.
func SortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// decodeAnyStrings reads dbt's two spellings of a tag list out of a decoded
// `config:` block: a single string, or a list of them.
func decodeAnyStrings(v any) []string {
	switch t := v.(type) {
	case string:
		if t == "" {
			return nil
		}
		return []string{t}
	case []string:
		return t
	case []any:
		out := make([]string, 0, len(t))
		for _, e := range t {
			if s, ok := e.(string); ok && s != "" {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}
