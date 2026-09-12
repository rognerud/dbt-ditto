// Package inherit builds the cross-project dependency graph and resolves what
package inherit

import (
	"sort"
	"strings"
	"sync"

	"github.com/rognerud/dbt-ditto/internal/dbt"
)

// Graph is the union of every loaded project's nodes, keyed by unique_id, with
// cross-manifest edges resolved.
type Graph struct {
	Nodes    map[string]*dbt.Node
	Projects []*dbt.Project

	// byName indexes nodes by lower-cased resource name, used to repair edges
	byName map[string][]*dbt.Node
	parent map[string][]string

	genMu sync.Mutex
	gens  map[string][][]*dbt.Node

	child      map[string][]string
	childOnce  sync.Once
	descMu     sync.Mutex
	descendant map[string][][]*dbt.Node
}

// BuildGraph merges the projects into a single graph. Projects are listed
func BuildGraph(projects []*dbt.Project) *Graph {
	g := &Graph{
		Nodes:    make(map[string]*dbt.Node),
		Projects: projects,
		byName:   make(map[string][]*dbt.Node),
		parent:   make(map[string][]string),
		gens:     make(map[string][][]*dbt.Node),
	}

	for _, p := range projects {
		for id, n := range p.Manifest.Nodes {
			g.add(id, n)
		}
		for id, n := range p.Manifest.Sources {
			g.add(id, n)
		}
	}

	for id, n := range g.Nodes {
		deps := make([]string, 0, len(n.DependsOn.Nodes))
		for _, dep := range n.DependsOn.Nodes {
			// dbt-osmosis only walks documentable ancestors.
			if !documentablePrefix(dep) {
				continue
			}
			if _, ok := g.Nodes[dep]; ok {
				deps = append(deps, dep)
				continue
			}
			// Cross-project ref: the upstream project may publish the node under a
			// different package name, so fall back to matching on the resource name.
			if resolved, ok := g.resolveByName(dep, n); ok {
				deps = append(deps, resolved)
			}
		}
		g.parent[id] = deps
	}
	return g
}

func documentablePrefix(id string) bool {
	return strings.HasPrefix(id, "model.") ||
		strings.HasPrefix(id, "seed.") ||
		strings.HasPrefix(id, "source.") ||
		strings.HasPrefix(id, "snapshot.")
}

func (g *Graph) add(id string, n *dbt.Node) {
	if _, exists := g.Nodes[id]; exists {
		return
	}
	g.Nodes[id] = n
	key := strings.ToLower(n.Name)
	g.byName[key] = append(g.byName[key], n)
}

// resolveByName maps an unresolvable dependency unique_id onto a node in another
// manifest. Only public models are eligible, matching dbt's own access rules.
func (g *Graph) resolveByName(depID string, from *dbt.Node) (string, bool) {
	parts := strings.Split(depID, ".")
	if len(parts) < 3 {
		return "", false
	}
	name := parts[len(parts)-1]

	candidates := g.byName[strings.ToLower(name)]
	var best *dbt.Node
	for _, c := range candidates {
		if c.UniqueID == from.UniqueID || c.Project == from.Project {
			continue
		}
		if c.ResourceType == "model" && c.Access != "public" {
			continue
		}
		if best == nil || c.UniqueID < best.UniqueID {
			best = c
		}
	}
	if best == nil {
		return "", false
	}
	return best.UniqueID, true
}

// Parents returns the resolved direct upstream unique_ids of a node.
func (g *Graph) Parents(id string) []string { return g.parent[id] }

// FindNode resolves a unique_id or a bare resource name as written in a `ref()`.
func (g *Graph) FindNode(ref string) (*dbt.Node, bool) {
	if n, ok := g.Nodes[ref]; ok {
		return n, true
	}
	candidates := g.byName[strings.ToLower(ref)]
	if len(candidates) == 1 {
		return candidates[0], true
	}
	return nil, false
}

// maxGenerations bounds the ancestor walk, as dbt-osmosis does, so a cyclic or
const maxGenerations = 100

// Generations returns the node's ancestors grouped by distance, nearest first, each
// group sorted by unique_id.
func (g *Graph) Generations(id string) [][]*dbt.Node {
	g.genMu.Lock()
	defer g.genMu.Unlock()
	if cached, ok := g.gens[id]; ok {
		return cached
	}
	out := g.levels(id, g.parent)
	g.gens[id] = out
	return out
}

// levels groups the nodes reachable from id through adj by the distance they were
// first reached at, nearest first, each group sorted by unique_id.
func (g *Graph) levels(id string, adj map[string][]string) [][]*dbt.Node {
	byDepth := map[int][]string{}
	visited := map[string]bool{id: true}
	var walk func(string, int)
	walk = func(from string, depth int) {
		if depth > maxGenerations {
			return
		}
		for _, next := range adj[from] {
			if visited[next] {
				continue
			}
			visited[next] = true
			if _, ok := g.Nodes[next]; !ok {
				continue
			}
			byDepth[depth] = append(byDepth[depth], next)
			walk(next, depth+1)
		}
	}
	walk(id, 1)

	// Depths are contiguous: a node is only recorded while walking one a level up.
	out := make([][]*dbt.Node, 0, len(byDepth))
	for d := 1; d <= len(byDepth); d++ {
		ids := byDepth[d]
		sort.Strings(ids)
		gen := make([]*dbt.Node, 0, len(ids))
		for _, nid := range ids {
			gen = append(gen, g.Nodes[nid])
		}
		out = append(out, gen)
	}
	return out
}

// Descendants returns the nodes downstream of id, grouped by distance and nearest
// first, mirroring Generations.
func (g *Graph) Descendants(id string) [][]*dbt.Node {
	g.childOnce.Do(func() {
		g.child = make(map[string][]string, len(g.parent))
		for node, parents := range g.parent {
			for _, p := range parents {
				g.child[p] = append(g.child[p], node)
			}
		}
		for _, children := range g.child {
			sort.Strings(children)
		}
	})

	g.descMu.Lock()
	defer g.descMu.Unlock()
	if cached, ok := g.descendant[id]; ok {
		return cached
	}

	out := g.levels(id, g.child)
	if g.descendant == nil {
		g.descendant = map[string][][]*dbt.Node{}
	}
	g.descendant[id] = out
	return out
}

// Ancestors is a flattened Generations, for callers that do not care which
// generation a node came from.
func (g *Graph) Ancestors(id string) []*dbt.Node {
	var out []*dbt.Node
	for _, gen := range g.Generations(id) {
		out = append(out, gen...)
	}
	return out
}
