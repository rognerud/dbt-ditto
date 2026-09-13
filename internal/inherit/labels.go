package inherit

import (
	"reflect"

	"github.com/rognerud/dbt-ditto/internal/config"
	"github.com/rognerud/dbt-ditto/internal/dbt"
)

// Warning kinds about labels a provider reported.
const (
	// WarnLabelAggregate: an aggregated column derives from one somebody labelled, and the
	// label was not carried across.
	WarnLabelAggregate = "label_aggregate"
	// WarnLabelConflict: two ancestors in one generation label the column
	// differently, and choosing alphabetically between `restricted` and `public` is
	// not something this tool should do quietly.
	WarnLabelConflict = "label_conflict"
)

// labelKey is the meta key provider labels live under, or empty when they are
// flattened, ignored, or routed only to tags.
func (r *Resolver) labelKey() string {
	if !r.Cfg.SourceLabels.Nested() {
		return ""
	}
	return r.Cfg.SourceLabels.MetaKey
}

// carryLabels decides whether an ancestor's label map travels to this column, and
// records why not when it does not.
func (r *Resolver) carryLabels(k *knowledge, from *dbt.Node, rest []*dbt.Node,
	name string, keys [matchNone][]string, rank matchRank, value any) bool {

	l := r.Cfg.SourceLabels
	switch rank {
	case matchExact:
	case matchLeaf:
		if !l.PropagateStructs {
			return false
		}
	default: // matchAggregate, matchLeafAggregate
		switch l.Aggregates {
		case config.AggregatesInherit:
		case config.AggregatesIgnore:
			return false
		default: // AggregatesWarn
			k.note(name, WarnLabelAggregate,
				"derives from a labelled column on %s; the labels were not carried across, because aggregating a value does not preserve what was said about it",
				from.UniqueID)
			return false
		}
	}

	if dissent, ok := r.labelDissent(rest, name, keys, value); ok {
		switch l.OnConflict {
		case config.ConflictFirst:
			return true
		case config.ConflictNone:
			return false
		default: // ConflictWarn
			k.note(name, WarnLabelConflict,
				"%s and %s label this column differently; nothing was written, because choosing between them alphabetically is not a decision this tool can make",
				from.UniqueID, dissent)
			return false
		}
	}
	return true
}

// labelDissent names the first ancestor in the rest of a generation whose
func (r *Resolver) labelDissent(rest []*dbt.Node, name string, keys [matchNone][]string, won any) (string, bool) {
	if r.Cfg.SourceLabels.OnConflict == config.ConflictNone {
		return "", false
	}
	key := r.labelKey()
	if key == "" {
		return "", false
	}
	dissent := ""
	r.eachMatch(rest, name, keys, func(a *dbt.Node, c *dbt.Column) bool {
		v, ok := c.EffectiveMeta().Get(key)
		if ok && !sameLabels(v, won) {
			dissent = a.UniqueID
			return false
		}
		return true
	})
	return dissent, dissent != ""
}

// sameLabels compares two label maps by content, since they arrive as
// *OrderedMap from the manifest and from a provider alike.
func sameLabels(a, b any) bool {
	am, aok := a.(*dbt.OrderedMap)
	bm, bok := b.(*dbt.OrderedMap)
	if !aok || !bok {
		return reflect.DeepEqual(a, b)
	}
	if am.Len() != bm.Len() {
		return false
	}
	for _, k := range am.Keys() {
		av, _ := am.Get(k)
		bv, ok := bm.Get(k)
		if !ok || !reflect.DeepEqual(av, bv) {
			return false
		}
	}
	return true
}
