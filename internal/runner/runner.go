// Package runner orchestrates a dbt-ditto run: load the projects, build the
package runner

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/rognerud/dbt-ditto/internal/config"
	"github.com/rognerud/dbt-ditto/internal/dbt"
	"github.com/rognerud/dbt-ditto/internal/inherit"
	"github.com/rognerud/dbt-ditto/internal/yamlfile"
	"gopkg.in/yaml.v3"
)

// Options are the per-invocation switches, mostly mirroring CLI flags.
type Options struct {
	DryRun   bool
	Check    bool
	Select   []string
	Organize bool // may be forced off from the CLI
	Verbose  bool

	// RefreshSources runs the configured source providers and rewrites the cache.
	RefreshSources bool
	// Target is the profiles.yml target providers should connect with.
	Target string
}

// Change is one recorded edit, for reporting.
type Change struct {
	Node   string
	File   string
	Detail string
}

// Report summarises a run.
type Report struct {
	Changes      []Change
	FilesWritten []string
	FilesDeleted []string
	NodesScanned int
	// Warnings are per-column notes: an unresolvable directive, or a column its
	// parents document differently. They never fail the run.
	Warnings []inherit.Warning
	// Notes are remarks about the run as a whole rather than about a column.
	Notes []string
}

// Run executes the whole pipeline.
func Run(cfg *config.Config, opts Options) (*Report, error) {
	resolved := cfg.Resolve()
	if !opts.Organize {
		resolved.Organize = false
	}

	// Set before anything is decoded: which extra column keys are kept is a
	// decoding decision, and the manifest is read once.
	dbt.ExtraColumnKeys = resolved.ExtraKeys

	projects, err := loadProjects(cfg)
	if err != nil {
		return nil, err
	}

	graph := inherit.BuildGraph(projects)

	// Folded in before resolution, so nothing downstream can tell a provider's
	// answer from a catalog dbt generated.
	notes, err := applySources(cfg, resolved, graph, opts)
	if err != nil {
		return nil, err
	}

	res := &inherit.Resolver{Graph: graph, Cfg: resolved}

	// Collected before anything is resolved: two declarations that disagree are a
	// question only a person can answer, and asking halfway through would leave
	// half the files written.
	definitives, err := inherit.BuildDefinitives(graph, res.Fold)
	if err != nil {
		return nil, err
	}
	res.Definitives = definitives

	targets := selectNodes(projects, opts.Select)
	rep := &Report{NodesScanned: len(targets), Notes: notes}
	rep.Warnings = append(rep.Warnings, shadowedSourceWarnings(graph, targets)...)

	// Work out which YAML file each node lives in now and which it should live in
	// after organising, then load every file involved in one parallel pass.
	type placement struct {
		node *dbt.Node
		from string // repo-relative, empty when undocumented
		to   string // repo-relative
	}
	placements := make([]placement, 0, len(targets))
	for _, n := range targets {
		from, _ := n.SchemaFile()
		to := from
		if resolved.Organize {
			if t, ok := TargetSchemaPath(n); ok {
				to = t
			}
		}
		if to == "" {
			// Undocumented and no path rule: fall back to a sibling file.
			to = defaultSchemaPath(n)
		}
		placements = append(placements, placement{node: n, from: from, to: to})
	}

	files := newFileSet()
	pathSet := map[string]bool{}
	for _, p := range placements {
		root := p.node.Project.Root
		if p.from != "" {
			pathSet[filepath.Join(root, p.from)] = true
		}
		pathSet[filepath.Join(root, p.to)] = true
	}
	paths := make([]string, 0, len(pathSet))
	for abs := range pathSet {
		paths = append(paths, abs)
	}

	loadErrs := make([]error, len(paths))
	loaded := make([]*yamlfile.File, len(paths))
	parallel(len(paths), func(i int) {
		loaded[i], loadErrs[i] = yamlfile.LoadOrNew(paths[i])
	})
	for i, err := range loadErrs {
		if err != nil {
			return nil, err
		}
		files.put(paths[i], loaded[i])
	}

	// Resolving is the expensive part and only reads, so it runs in parallel.
	docs := make([]*inherit.NodeDoc, len(placements))
	parallel(len(placements), func(i int) {
		p := placements[i]
		var existing inherit.Existing
		if p.from != "" {
			if src := files.get(filepath.Join(p.node.Project.Root, p.from)); src != nil {
				existing = readExisting(src, p.node)
			}
		}
		docs[i] = res.Resolve(p.node, existing)
	})

	for i, p := range placements {
		root := p.node.Project.Root
		fromAbs := ""
		if p.from != "" {
			fromAbs = filepath.Join(root, p.from)
		}
		toAbs := filepath.Join(root, p.to)

		dst := files.get(toAbs)
		if dst == nil {
			continue
		}
		doc := docs[i]
		rep.Warnings = append(rep.Warnings, doc.Warnings...)

		if fromAbs != "" && fromAbs != toAbs {
			if src := files.get(fromAbs); src != nil {
				if entry := findEntry(src, p.node); entry != nil {
					carryOver(dst, p.node, entry)
					removeEntry(src, p.node)
					rep.Changes = append(rep.Changes, Change{
						Node:   p.node.UniqueID,
						File:   p.to,
						Detail: fmt.Sprintf("moved from %s", p.from),
					})
				}
			}
		}

		wopts := writeOpts{cfg: resolved, configBlock: configBlockFor(p.node)}
		if changes := writeDoc(dst, p.node, doc, wopts); len(changes) > 0 {
			for _, c := range changes {
				rep.Changes = append(rep.Changes, Change{Node: p.node.UniqueID, File: p.to, Detail: c})
			}
		}
	}

	// Save, and drop files organising emptied out.
	abs := files.paths()
	type outcome struct {
		rel     string
		written bool
		deleted bool
		err     error
	}
	outcomes := make([]outcome, len(abs))
	parallel(len(abs), func(i int) {
		path := abs[i]
		f := files.get(path)
		o := outcome{rel: relTo(projects, path)}

		if f.IsEmpty() {
			if f.Created {
				outcomes[i] = o // never existed, nothing to write or delete
				return
			}
			if resolved.DeleteEmpty {
				if !opts.DryRun {
					if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
						o.err = err
						outcomes[i] = o
						return
					}
				}
				o.deleted = true
				outcomes[i] = o
				return
			}
		}

		o.written, o.err = f.Save(opts.DryRun)
		outcomes[i] = o
	})

	for _, o := range outcomes {
		if o.err != nil {
			return nil, o.err
		}
		switch {
		case o.deleted:
			rep.FilesDeleted = append(rep.FilesDeleted, o.rel)
		case o.written:
			rep.FilesWritten = append(rep.FilesWritten, o.rel)
		}
	}

	sort.Strings(rep.FilesWritten)
	sort.Strings(rep.FilesDeleted)
	return rep, nil
}

// parallel runs body for every index in [0, n), one goroutine per CPU.
func parallel(n int, body func(i int)) {
	if n == 0 {
		return
	}
	workers := runtime.GOMAXPROCS(0)
	if workers > n {
		workers = n
	}
	if workers <= 1 {
		for i := 0; i < n; i++ {
			body(i)
		}
		return
	}

	var next atomic.Int64
	var wg sync.WaitGroup
	wg.Add(workers)
	for w := 0; w < workers; w++ {
		go func() {
			defer wg.Done()
			for {
				i := int(next.Add(1)) - 1
				if i >= n {
					return
				}
				body(i)
			}
		}()
	}
	wg.Wait()
}

// configBlockFor decides whether a node's column meta and tags belong inside a
// `config:` block, from the dbt version that produced the manifest, as dbt-osmosis'
// fusion_compat detection does.
func configBlockFor(n *dbt.Node) bool {
	if n.Manifest == nil {
		return false
	}
	return n.Manifest.WantsConfigBlock()
}

// loadProjects reads every configured project in parallel.
func loadProjects(cfg *config.Config) ([]*dbt.Project, error) {
	out := make([]*dbt.Project, len(cfg.Projects))
	errs := make([]error, len(cfg.Projects))
	parallel(len(cfg.Projects), func(i int) {
		ref := cfg.Projects[i]
		// A manifest-only ref has no project directory to read dbt_project.yml from.
		if ref.Manifest != "" {
			out[i], errs[i] = dbt.LoadManifestOnly(ref.Name, absTo(cfg.Dir, ref.Manifest))
			if errs[i] != nil {
				errs[i] = fmt.Errorf("project %s: %w", ref.Manifest, errs[i])
			}
			return
		}
		out[i], errs[i] = dbt.LoadProject(absTo(cfg.Dir, ref.Path), ref.Target, !ref.Upstream)
		if errs[i] != nil {
			errs[i] = fmt.Errorf("project %s: %w", ref.Path, errs[i])
		}
	})
	for _, err := range errs {
		if err != nil {
			return nil, err
		}
	}
	return dropDuplicateManifests(out, cfg.Projects), nil
}

// dropDuplicateManifests removes a manifest-only project naming a project already
// loaded from a checkout, or named twice.
func dropDuplicateManifests(projects []*dbt.Project, refs []config.ProjectRef) []*dbt.Project {
	fromCheckout := make(map[string]bool, len(projects))
	for i, p := range projects {
		if p != nil && refs[i].Manifest == "" {
			fromCheckout[p.Name] = true
		}
	}
	out := make([]*dbt.Project, 0, len(projects))
	seen := make(map[string]bool, len(projects))
	for i, p := range projects {
		if refs[i].Manifest != "" && (fromCheckout[p.Name] || seen[p.Name]) {
			continue
		}
		seen[p.Name] = true
		out = append(out, p)
	}
	return out
}

// selectNodes returns the writable, documentable nodes matching the selector.
func selectNodes(projects []*dbt.Project, selectors []string) []*dbt.Node {
	var out []*dbt.Node
	for _, p := range projects {
		if !p.Writable {
			continue
		}
		for _, m := range []map[string]*dbt.Node{p.Manifest.Nodes, p.Manifest.Sources} {
			for _, n := range m {
				if !n.Documentable() || n.PackageName != p.Name {
					continue
				}
				if !matches(n, selectors) {
					continue
				}
				out = append(out, n)
			}
		}
	}
	// Entries are written in manifest order, which is the order dbt records nodes in and
	// therefore the order dbt-osmosis lays out a schema file.
	sort.Slice(out, func(i, j int) bool {
		if out[i].Manifest != out[j].Manifest {
			return out[i].UniqueID < out[j].UniqueID
		}
		return out[i].Order < out[j].Order
	})
	return out
}

// matches applies the `--select` filters: a bare name, `tag:x`, `path:x` or a
func matches(n *dbt.Node, selectors []string) bool {
	if len(selectors) == 0 {
		return true
	}
	fqn := strings.Join(n.FQN, ".")
	for _, s := range selectors {
		switch {
		case strings.HasPrefix(s, "tag:"):
			want := strings.TrimPrefix(s, "tag:")
			for _, t := range n.Tags {
				if t == want {
					return true
				}
			}
		case strings.HasPrefix(s, "path:"):
			if strings.HasPrefix(n.OriginalFilePath, strings.TrimPrefix(s, "path:")) {
				return true
			}
		default:
			if globMatch(s, n.Name) || globMatch(s, fqn) || n.UniqueID == s {
				return true
			}
		}
	}
	return false
}

func globMatch(pattern, s string) bool {
	ok, err := filepath.Match(pattern, s)
	return err == nil && ok
}

// defaultSchemaPath is where an undocumented node goes when no path rule says
// otherwise: a `_<name>.yml` beside the model file.
func defaultSchemaPath(n *dbt.Node) string {
	src := filepath.ToSlash(n.OriginalFilePath)
	if n.IsSource() {
		return src
	}
	return filepath.ToSlash(filepath.Join(filepath.Dir(src), "_"+n.Name+".yml"))
}

func absTo(dir, path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(dir, path)
}

func relTo(projects []*dbt.Project, abs string) string {
	for _, p := range projects {
		if rel, err := filepath.Rel(p.Root, abs); err == nil && !strings.HasPrefix(rel, "..") {
			return filepath.ToSlash(filepath.Join(filepath.Base(p.Root), rel))
		}
	}
	return abs
}

// fileSet is a concurrency-safe map of loaded YAML files.
type fileSet struct {
	mu sync.Mutex
	m  map[string]*yamlfile.File
}

func newFileSet() *fileSet { return &fileSet{m: map[string]*yamlfile.File{}} }

func (s *fileSet) put(k string, f *yamlfile.File) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[k] = f
}

func (s *fileSet) get(k string) *yamlfile.File {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.m[k]
}

func (s *fileSet) paths() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.m))
	for k := range s.m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// section returns the YAML top-level key a node's entry lives under.
func section(n *dbt.Node) string {
	switch n.ResourceType {
	case "seed":
		return "seeds"
	case "snapshot":
		return "snapshots"
	default:
		return "models"
	}
}

func findEntry(f *yamlfile.File, n *dbt.Node) *yaml.Node {
	if n.IsSource() {
		return f.SourceTable(n.SourceName, n.Relation(), false)
	}
	return f.Entry(section(n), n.Name, false)
}

func ensureEntry(f *yamlfile.File, n *dbt.Node) *yaml.Node {
	if n.IsSource() {
		return f.SourceTable(n.SourceName, n.Relation(), true)
	}
	return f.Entry(section(n), n.Name, true)
}

func removeEntry(f *yamlfile.File, n *dbt.Node) {
	if n.IsSource() {
		return // sources are never moved between files
	}
	f.RemoveEntry(section(n), n.Name)
}

// carryOver copies an entry verbatim, so a move keeps tests, comments and any
// keys dbt-ditto does not manage.
func carryOver(dst *yamlfile.File, n *dbt.Node, entry *yaml.Node) {
	target := ensureEntry(dst, n)
	for i := 0; i+1 < len(entry.Content); i += 2 {
		key := entry.Content[i].Value
		if key == "name" {
			continue
		}
		yamlfile.MapSet(target, key, entry.Content[i+1])
	}
}
