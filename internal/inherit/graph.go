// Package inherit builds the cross-project dependency graph and resolves what
// each node's documentation should be after inheritance.
package inherit

import (
	"sort"
	"strings"
	"sync"

	"github.com/rognerud/dbt-ditto/internal/dbt"
)

// Graph is the union of every loaded project's nodes, keyed by unique_id, with
// cross-manifest edges resolved. This is the piece that replaces dbt-loom: an
// upstream project's manifest is loaded alongside the downstream one and their
// dependency edges are stitched together, so a downstream model can inherit
// from a model that lives in another repository.
type Graph struct {
	Nodes    map[string]*dbt.Node
	Projects []*dbt.Project

	// byName indexes nodes by lower-cased resource name, used to repair edges
	// whose target unique_id is not literally present in any manifest.
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
// upstream-first only for tie-breaking; edge direction comes from depends_on.
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
			// dbt-osmosis only walks documentable ancestors; tests and other
			// resource types are not sources of column knowledge.
			if !documentablePrefix(dep) {
				continue
			}
			if _, ok := g.Nodes[dep]; ok {
				deps = append(deps, dep)
				continue
			}
			// Cross-project ref: dbt records the upstream unique_id, but the
			// upstream project may publish it under a different package name.
			// Fall back to matching on the resource name.
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

// resolveByName maps an unresolvable dependency unique_id onto a node in
// another manifest. Only public models are eligible, matching dbt's own
// cross-project access rules.
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

// FindNode resolves a human-written node reference: either a unique_id, or a
// bare resource name as it would be written in a `ref()`. The second return
// value is false when the name matches more than one node, since silently
// picking one of them is how a directive ends up meaning the opposite of what
// it says.
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
// pathological graph cannot hang the run.
const maxGenerations = 100

// Generations returns the node's ancestors grouped by distance, nearest first:
// index 0 holds the direct parents, index 1 their parents, and so on. Each
// group is sorted by unique_id.
//
// The grouping deliberately reproduces dbt-osmosis' ancestor tree: the walk is
// depth-first with a single shared visited set, so a node reachable by several
// routes is filed under the depth at which the walk first reached it, not its
// shortest path. Getting this wrong changes which ancestor wins a conflict.
func (g *Graph) Generations(id string) [][]*dbt.Node {
	g.genMu.Lock()
	defer g.genMu.Unlock()
	if cached, ok := g.gens[id]; ok {
		return cached
	}

	byDepth := map[int][]string{}
	visited := map[string]bool{id: true}
	g.walk(id, 1, byDepth, visited)

	maxDepth := 0
	for d := range byDepth {
		if d > maxDepth {
			maxDepth = d
		}
	}
	out := make([][]*dbt.Node, 0, maxDepth)
	for d := 1; d <= maxDepth; d++ {
		ids := byDepth[d]
		if len(ids) == 0 {
			continue
		}
		sort.Strings(ids)
		gen := make([]*dbt.Node, 0, len(ids))
		for _, pid := range ids {
			if n, ok := g.Nodes[pid]; ok {
				gen = append(gen, n)
			}
		}
		out = append(out, gen)
	}
	g.gens[id] = out
	return out
}

func (g *Graph) walk(id string, depth int, byDepth map[int][]string, visited map[string]bool) {
	if depth > maxGenerations {
		return
	}
	for _, dep := range g.parent[id] {
		if visited[dep] {
			continue
		}
		visited[dep] = true
		if _, ok := g.Nodes[dep]; !ok {
			continue
		}
		byDepth[depth] = append(byDepth[depth], dep)
		g.walk(dep, depth+1, byDepth, visited)
	}
}

// Descendants returns the nodes downstream of id, grouped by distance and
// nearest first, mirroring Generations in the other direction.
//
// A source table has nothing above it, so the only documentation that can exist
// in the graph is below it: the staging model that reads it. Walking downwards
// is what lets that documentation be carried back up into the source.
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

	var out [][]*dbt.Node
	visited := map[string]bool{id: true}
	level := append([]string(nil), g.child[id]...)
	for depth := 0; depth < maxGenerations && len(level) > 0; depth++ {
		var gen []*dbt.Node
		var next []string
		for _, cid := range level {
			if visited[cid] {
				continue
			}
			visited[cid] = true
			if n, ok := g.Nodes[cid]; ok {
				gen = append(gen, n)
			}
			next = append(next, g.child[cid]...)
		}
		if len(gen) > 0 {
			sort.Slice(gen, func(i, j int) bool { return gen[i].UniqueID < gen[j].UniqueID })
			out = append(out, gen)
		}
		sort.Strings(next)
		level = next
	}

	if g.descendant == nil {
		g.descendant = map[string][][]*dbt.Node{}
	}
	g.descendant[id] = out
	return out
}

// Ancestors returns every upstream node reachable from id, nearest generation
// first. It is a flattened Generations, kept for callers that do not care which
// generation a node came from.
func (g *Graph) Ancestors(id string) []*dbt.Node {
	var out []*dbt.Node
	for _, gen := range g.Generations(id) {
		out = append(out, gen...)
	}
	return out
}
