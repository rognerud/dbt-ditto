package inherit

import (
	"sort"
	"strings"

	"github.com/rognerud/dbt-ditto/internal/dbt"
)

// ShadowedSource is a source table that a loaded project also builds.
//
// This is the pre-loom pattern: rather than depending on the upstream project,
// a team declares its output as a `source:` and points at the relation. The two
// are indistinguishable in the graph — a source has no `depends_on`, so both a
// genuine external table and a shadowed one are roots — but they are not
// indistinguishable by relation, and they want opposite treatment. The shadowed
// one already has documentation one `ref()` away.
type ShadowedSource struct {
	Source *dbt.Node
	// By is the node that actually builds the relation.
	By *dbt.Node
}

// Mistake reports whether the shadow is worth telling someone about.
//
// A source backed by a *seed* is a deliberate pattern, not an error: seeds are
// how a project stands up fake raw data for development and testing, and the
// source declaration is what the rest of the project reads it through. Both
// halves are meant to exist. A source backed by a *model* is the pre-loom
// workaround, where the right answer is a cross-project ref.
//
// Either way the source is not external, so neither is ever sent to a provider.
func (s ShadowedSource) Mistake() bool { return s.By.ResourceType != "seed" }

// ClassifySources splits every source in the graph into the ones no loaded
// project builds and the ones some project does.
//
// The external set is what a source provider is asked about: it is exactly the
// set of nodes that inheritance can never reach, because a source is a DAG root
// and nothing upstream of it exists. Everything else in the graph either has
// ancestors or is a mistake worth reporting.
//
// Note that a dbt-loom upstream is never in either set. Loom-injected upstreams
// arrive as `model.` nodes, because the downstream project `ref()`s them; only
// a table declared with `source:` is a source here.
func (g *Graph) ClassifySources() (external []*dbt.Node, shadowed []ShadowedSource) {
	// Relations a loaded project builds. Ephemeral models are excluded: they are
	// inlined as CTEs and never materialize, so a source cannot be pointing at
	// one and a name collision with a real table is coincidence.
	built := make(map[string]*dbt.Node, len(g.Nodes))
	for _, n := range g.Nodes {
		if n.IsSource() || isEphemeral(n) {
			continue
		}
		key := relationKey(n.Database, n.Schema, n.Relation())
		// Lowest unique_id wins, so the report is stable when two projects
		// somehow build the same relation.
		if prev, ok := built[key]; !ok || n.UniqueID < prev.UniqueID {
			built[key] = n
		}
	}

	for _, n := range g.Nodes {
		if !n.IsSource() {
			continue
		}
		if by, ok := built[relationKey(n.Database, n.Schema, n.Relation())]; ok {
			shadowed = append(shadowed, ShadowedSource{Source: n, By: by})
			continue
		}
		external = append(external, n)
	}

	sort.Slice(external, func(i, j int) bool { return external[i].UniqueID < external[j].UniqueID })
	sort.Slice(shadowed, func(i, j int) bool {
		return shadowed[i].Source.UniqueID < shadowed[j].Source.UniqueID
	})
	return external, shadowed
}

// isEphemeral reports whether dbt inlines the node rather than materializing it.
func isEphemeral(n *dbt.Node) bool {
	m, ok := n.ConfigString("materialized")
	return ok && strings.EqualFold(m, "ephemeral")
}

// relationKey identifies a warehouse relation. Database is part of it: the same
// schema and table name in another database is another table, and treating them
// as one would report a shadow that does not exist.
func relationKey(database, schema, relation string) string {
	return strings.ToLower(database) + "." + strings.ToLower(schema) + "." + strings.ToLower(relation)
}
