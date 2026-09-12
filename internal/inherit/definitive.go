package inherit

import (
	"fmt"
	"sort"
	"strings"

	"github.com/rognerud/dbt-ditto/internal/dbt"
)

// DefinitiveKey marks a column's description as the settled wording for that
// column name, everywhere.
//
//	columns:
//	  - name: customer_id
//	    description: Surrogate key of the customer.
//	    meta:
//	      dbt_ditto_definitive: true
//
// A definitive column is a decision written down, for when several ancestors
// disagree: its description is copied to every column of that name in the
// graph — up the DAG as well as down it, and across project boundaries — and it
// outranks name matching, directives and `force`.
//
// The key is not configurable: it is a contract between repositories, and a
// contract each side spells differently is not one.
const DefinitiveKey = "dbt_ditto_definitive"

// Definitive is one column that has been declared settled.
type Definitive struct {
	// Node is the unique_id of the node holding the declaration, and is what an
	// inheriting column records as its progenitor.
	Node string
	// Column is the column name as the declaration spells it.
	Column string
	// Description is the settled wording.
	Description string
}

// ConflictError is two or more definitive declarations for the same column that
// do not agree. There is no sensible way to pick between them — that is the
// whole point of the marker — so the run stops and names them all.
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

// BuildDefinitives collects every definitive declaration in the graph, keyed by
// the folded column name, and fails on a disagreement.
//
// Declarations in a manifest-only project (a dbt-loom upstream) are ignored: a
// definitive is a decision this repository makes about its own documentation,
// and an injected manifest cannot be reviewed or edited here.
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

// disagreement reports a conflict when the claims for one column do not all say
// the same thing. Identical wording declared in several places is not a
// conflict: it is the same decision, written down more than once.
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

// isDefinitive reports whether a column carries the marker. Only a true boolean
// counts: `dbt_ditto_definitive: false` is somebody explicitly saying no.
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
