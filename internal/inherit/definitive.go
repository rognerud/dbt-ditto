package inherit

import (
	"fmt"
	"sort"
	"strings"

	"github.com/rognerud/dbt-ditto/internal/dbt"
)

// DefinitiveKey (`meta: {dbt_ditto_definitive: true}`) marks a column's description as
// the settled wording for that column name everywhere: it is copied to every column of
// that name in the graph, up the DAG as well as down and across project boundaries,
// outranking name matching, directives and `force`.
const DefinitiveKey = "dbt_ditto_definitive"

// Definitive is one column that has been declared settled.
type Definitive struct {
	// Node is the unique_id of the node holding the declaration, and is what an
	Node string
	// Column is the column name as the declaration spells it.
	Column string
	// Description is the settled wording.
	Description string
}

// ConflictError is two or more definitive declarations for one column that do not
// agree.
type ConflictError struct {
	Column   string
	Claims   []Definitive
	Insensit bool
}

func (e *ConflictError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "conflicting %s declarations for column %q:\n", DefinitiveKey, e.Column)
	for _, c := range e.Claims {
		fmt.Fprintf(&b, "  %s.%s: %q\n", c.Node, c.Column, c.Description)
	}
	if e.Insensit && !sameSpelling(e.Claims) {
		b.WriteString("  (these are the same column because inheritance.case_insensitive is on)\n")
	}
	b.WriteString("  resolve it by leaving one declaration, or by making them agree word for word")
	return b.String()
}

func sameSpelling(claims []Definitive) bool {
	for _, c := range claims[1:] {
		if c.Column != claims[0].Column {
			return false
		}
	}
	return true
}

// BuildDefinitives collects every definitive declaration in the graph, keyed by folded
// column name, and fails on a disagreement.
func BuildDefinitives(g *Graph, fold func(string) string) (map[string]Definitive, error) {
	claims := map[string][]Definitive{}
	for _, n := range g.Nodes {
		if n.Project != nil && n.Project.ManifestOnly {
			continue
		}
		for _, c := range n.Columns {
			if c == nil || !isDefinitive(c) {
				continue
			}
			claims[fold(c.Name)] = append(claims[fold(c.Name)], Definitive{
				Node:        n.UniqueID,
				Column:      c.Name,
				Description: c.Description,
			})
		}
	}

	out := make(map[string]Definitive, len(claims))
	var conflicts []string
	for key, list := range claims {
		sort.Slice(list, func(i, j int) bool {
			if list[i].Node != list[j].Node {
				return list[i].Node < list[j].Node
			}
			return list[i].Column < list[j].Column
		})
		if err := disagreement(key, list, fold); err != nil {
			conflicts = append(conflicts, err.Error())
			continue
		}
		out[key] = list[0]
	}
	if len(conflicts) > 0 {
		sort.Strings(conflicts)
		return nil, fmt.Errorf("%s", strings.Join(conflicts, "\n"))
	}
	return out, nil
}

// disagreement reports a conflict when the claims for one column differ.
func disagreement(key string, list []Definitive, fold func(string) string) error {
	for _, c := range list[1:] {
		if c.Description != list[0].Description {
			return &ConflictError{
				Column:   key,
				Claims:   list,
				Insensit: fold("A") == fold("a"),
			}
		}
	}
	return nil
}

// isDefinitive reports whether a column carries the marker.
func isDefinitive(c *dbt.Column) bool {
	m := c.EffectiveMeta()
	if m == nil {
		return false
	}
	v, ok := m.Get(DefinitiveKey)
	if !ok {
		return false
	}
	b, ok := v.(bool)
	return ok && b
}
