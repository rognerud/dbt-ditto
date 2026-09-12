package inherit

import (
	"strings"
	"sync"

	"github.com/rognerud/dbt-ditto/internal/config"
	"github.com/rognerud/dbt-ditto/internal/dbt"
)

// Matching a downstream column to an upstream one is tried in this order, and
// the first strategy that finds anything wins. Ordering matters more than the
// individual strategies: a column that shares its name with an upstream column
// must never be given a guess instead, however plausible the guess.
type matchRank int

const (
	// The names are the same, allowing for case.
	matchExact matchRank = iota
	// The last dotted segments agree: `profile.first_name` and `first_name` are
	// the same field, packed or unpacked.
	matchLeaf
	// The names agree once an aggregate word is stripped from either side:
	// `total_amount_cents` and `amount_cents`.
	matchAggregate
	// Both of the above at once, e.g. `profile.amount_cents` and `total_amount_cents`.
	matchLeafAggregate
	matchNone
)

// columnIndex holds one ancestor's columns keyed by every name a descendant
// might plausibly know them by. It is built once per ancestor and reused across
// every node that inherits from it, which matters because a widely used staging
// model is an ancestor of hundreds of others.
type columnIndex struct {
	// One map per rank: the rank records how far the alias is from the
	// column's real name.
	byRank [matchNone]map[string]*dbt.Column
}

// rankPairs is the order in which a downstream alias is tried against an
// upstream alias, cheapest total distance first.
//
// Both sides need aliasing, and independently. Packing `amount_cents` into a
// struct renames it downstream; summing it renames it downstream; the upstream
// column may itself already be a struct field. So a match is a pair of
// derivations, one applied at each end, and the pair with the least total
// distance wins. Exact/exact costs nothing and therefore always wins, which is
// the property that matters: a column that shares a name with an upstream
// column is never given a guess instead.
var rankPairs = func() [][2]matchRank {
	var pairs [][2]matchRank
	for total := 0; total <= 2*int(matchNone-1); total++ {
		for down := matchExact; down < matchNone; down++ {
			for up := matchExact; up < matchNone; up++ {
				if int(down)+int(up) == total {
					pairs = append(pairs, [2]matchRank{down, up})
				}
			}
		}
	}
	return pairs
}()

func (i *columnIndex) lookup(keys [matchNone][]string) (*dbt.Column, matchRank) {
	for _, pair := range rankPairs {
		down, up := pair[0], pair[1]
		m := i.byRank[up]
		if m == nil {
			continue
		}
		for _, key := range keys[down] {
			if c, ok := m[key]; ok {
				if down > up {
					return c, down
				}
				return c, up
			}
		}
	}
	return nil, matchNone
}

// matcher derives the alternative names a column may be known by, under the
// configured rules.
type matcher struct {
	cfg config.Resolved

	indexes sync.Map // *dbt.Node -> *columnIndex
}

func newMatcher(cfg config.Resolved) *matcher { return &matcher{cfg: cfg} }

// enabled reports whether any derived matching is configured. When it is not,
// the matcher is bypassed entirely and behaviour is dbt-osmosis'.
func (m *matcher) enabled() bool {
	return m.cfg.DerivedStructs || m.cfg.DerivedAggregates
}

func (m *matcher) fold(s string) string {
	if m.cfg.CaseInsensitive {
		return strings.ToLower(s)
	}
	return s
}

// leaf returns the last segment of a dotted column path.
func leaf(name string) string {
	if i := strings.LastIndexByte(name, '.'); i >= 0 {
		return name[i+1:]
	}
	return name
}

// stripAggregate returns the name with a single leading or trailing aggregate
// word removed, in every combination that applies. The name itself is not
// included.
//
// Stripping is limited to one word at each end: going further turns
// `max_order_count` into `order`, which is no longer obviously the same thing,
// and a wrong description is worse than none.
func (m *matcher) stripAggregate(name string) []string {
	if name == "" || !m.cfg.DerivedAggregates {
		return nil
	}

	var out []string
	add := func(s string) {
		if s == "" || s == name {
			return
		}
		for _, existing := range out {
			if existing == s {
				return
			}
		}
		out = append(out, s)
	}

	trimmedPrefix := ""
	for _, p := range m.cfg.DerivedPrefixes {
		if rest, ok := trimWord(name, p, true); ok {
			add(rest)
			if trimmedPrefix == "" {
				trimmedPrefix = rest
			}
		}
	}
	for _, s := range m.cfg.DerivedSuffixes {
		if rest, ok := trimWord(name, s, false); ok {
			add(rest)
			// `total_amount_cents_sum` loses both ends.
			if trimmedPrefix != "" {
				if both, ok := trimWord(trimmedPrefix, s, false); ok {
					add(both)
				}
			}
		}
	}
	return out
}

// trimWord removes `word_` from the front or `_word` from the back. Only whole
// underscore-separated words count, so `counterparty` keeps its `count`.
func trimWord(name, word string, prefix bool) (string, bool) {
	if word == "" {
		return "", false
	}
	if prefix {
		affix := word + "_"
		if len(name) > len(affix) && strings.EqualFold(name[:len(affix)], affix) {
			return name[len(affix):], true
		}
		return "", false
	}
	affix := "_" + word
	if len(name) > len(affix) && strings.EqualFold(name[len(name)-len(affix):], affix) {
		return name[:len(name)-len(affix)], true
	}
	return "", false
}

// eachKey calls yield with every folded lookup key a column name can be found
// under, and the rank at which that key counts as a match. keysFor and indexFor
// are the two sides of the same lookup, so they derive their keys from here.
func (m *matcher) eachKey(name string, yield func(matchRank, string)) {
	yield(matchExact, m.fold(name))
	if !m.enabled() {
		return
	}
	l := ""
	if m.cfg.DerivedStructs {
		if s := leaf(name); s != name {
			l = s
			yield(matchLeaf, m.fold(l))
		}
	}
	for _, stripped := range m.stripAggregate(name) {
		yield(matchAggregate, m.fold(stripped))
	}
	for _, stripped := range m.stripAggregate(l) {
		yield(matchLeafAggregate, m.fold(stripped))
	}
}

// keysFor returns the lookup keys for a downstream column name, grouped by the
// rank at which each may match.
func (m *matcher) keysFor(name string) [matchNone][]string {
	var keys [matchNone][]string
	m.eachKey(name, func(rank matchRank, key string) {
		keys[rank] = append(keys[rank], key)
	})
	return keys
}

// indexFor builds, and then remembers, the lookup index for one ancestor.
func (m *matcher) indexFor(n *dbt.Node) *columnIndex {
	if cached, ok := m.indexes.Load(n); ok {
		return cached.(*columnIndex)
	}

	idx := &columnIndex{}
	put := func(rank matchRank, key string, c *dbt.Column) {
		if key == "" {
			return
		}
		if idx.byRank[rank] == nil {
			idx.byRank[rank] = map[string]*dbt.Column{}
		}
		// First writer wins, so a column earlier in the node keeps the key
		// rather than a later one silently taking it over.
		if _, taken := idx.byRank[rank][key]; !taken {
			idx.byRank[rank][key] = c
		}
	}

	for name, c := range n.Columns {
		m.eachKey(name, func(rank matchRank, key string) { put(rank, key, c) })
	}

	actual, _ := m.indexes.LoadOrStore(n, idx)
	return actual.(*columnIndex)
}

// find returns the ancestor column a downstream column should inherit from,
// and how far the match had to travel. The rank matters beyond bookkeeping: a
// description survives being summed and a classification does not, so it
// decides what is carried across.
//
// The last return value reports whether the ancestor has the column at all,
// even undocumented, which is what decides whether the ancestor claims the
// column for its generation and shadows everything behind it.
func (m *matcher) find(a *dbt.Node, name string, keys [matchNone][]string) (*dbt.Column, matchRank, bool) {
	if c, rank := m.indexFor(a).lookup(keys); rank != matchNone {
		return c, rank, true
	}
	// Nothing in the manifest, but the warehouse may still have the column: an
	// undocumented ancestor still shadows its own ancestors.
	if c := a.EffectiveColumn(name, m.cfg.CaseInsensitive); c != nil {
		return c, matchExact, true
	}
	return nil, matchNone, false
}
