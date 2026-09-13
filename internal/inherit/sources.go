package inherit

import (
	"sort"
	"strings"

	"github.com/rognerud/dbt-ditto/internal/dbt"
)

// ShadowedSource is a source table a loaded project also builds: the pre-loom pattern
// of declaring an upstream project's output as a `source:`.
type ShadowedSource struct {
	Source *dbt.Node
	// By is the node that actually builds the relation.
	By *dbt.Node
}

// Mistake reports whether the shadow is worth telling someone about: a source
// backed by a seed is the deliberate fake-raw-data pattern, one backed by a
// model the pre-loom workaround. Neither is external.
func (s ShadowedSource) Mistake() bool { return s.By.ResourceType != "seed" }

// ClassifySources splits sources into the ones no loaded project builds — what a
// provider is asked about, being exactly what inheritance cannot reach — and the ones
// some project does.
func (g *Graph) ClassifySources() (external []*dbt.Node, shadowed []ShadowedSource) {
	// Relations a loaded project builds. Ephemeral models never materialize.
	built := make(map[string]*dbt.Node, len(g.Nodes))
	for _, n := range g.Nodes {
		if n.IsSource() || isEphemeral(n) {
			continue
		}
		key := relationKey(n.Database, n.Schema, n.Relation())
		// Lowest unique_id wins, so the report is stable.
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
func relationKey(database, schema, relation string) string {
	return strings.ToLower(database) + "." + strings.ToLower(schema) + "." + strings.ToLower(relation)
}
