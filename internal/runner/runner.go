// Package runner orchestrates a dbt-ditto run: load the projects, build the
package runner

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/rognerud/dbt-ditto/internal/config"
	"github.com/rognerud/dbt-ditto/internal/dbt"
	"github.com/rognerud/dbt-ditto/internal/inherit"
	"github.com/rognerud/dbt-ditto/internal/par"
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
type Change struct{ Node, File, Detail string }

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
	// Checked here as well as in Load, so a configuration assembled in code — a
	// test, or `inherit <dir>` — cannot reach the files with a rule that has no
	// meaning.
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
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
	external, shadowed := graph.ClassifySources()

	// Folded in before resolution, so nothing downstream can tell a provider's
	// answer from a catalog dbt generated.
	notes, err := applySources(cfg, resolved, external, opts)
	if err != nil {
		return nil, err
	}

	res := &inherit.Resolver{Graph: graph, Cfg: resolved}

	targets := selectNodes(projects, opts.Select)
	rep := &Report{NodesScanned: len(targets), Notes: notes}
	rep.Warnings = append(rep.Warnings, shadowedSourceWarnings(shadowed, targets)...)

	// Work out which YAML file each node lives in now and which it should live in
	// after organising, then load every file involved in one parallel pass.
	type placement struct {
		node *dbt.Node
		from string // repo-relative, empty when the manifest knows of no schema file
		to   string // repo-relative
	}
	// current is the file holding the node's documentation as it stands, which is
	// not always the one the manifest names.
	//
	// `from` comes from `patch_path`, and that is only as old as the last
	// `dbt parse`: a node documented since — by a person, or by a previous
	// dbt-ditto run — has a schema file dbt has never seen, so `patch_path` is
	// null and `from` is empty. Resolving such a node against nothing treats
	// every key in that file as absent, and since the writer rebuilds `meta:`
	// from what was resolved, the file's own meta is deleted and re-derived on
	// every run. That is what removed a `dbt_ditto_definitive` somebody typed
	// instead of settling the column with it.
	//
	// It can also be stale in the other direction: organising moves an entry and
	// deletes the file it came from, while `patch_path` goes on naming that file
	// until dbt parses again. Reading it then finds nothing, so every column
	// looks new and every description is reported as an edit — against a file
	// that ends up byte for byte identical, which is the "edits reported, no
	// change in git" this used to produce on every run after the first.
	//
	// So the file holding the node's entry is the one that is read, whichever of
	// the two that turns out to be.
	current := func(p placement, files map[string]*yamlfile.File) *yamlfile.File {
		for _, rel := range []string{p.from, p.to} {
			if rel == "" {
				continue
			}
			f := files[filepath.Join(p.node.Project.Root, rel)]
			if f != nil && entryFor(f, p.node, false) != nil {
				return f
			}
		}
		return nil
	}
	placements := make([]placement, 0, len(targets))
	pathSet := map[string]bool{}
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
		if from != "" {
			pathSet[filepath.Join(n.Project.Root, from)] = true
		}
		pathSet[filepath.Join(n.Project.Root, to)] = true
	}
	paths := slices.Sorted(maps.Keys(pathSet))

	// files is written serially here and only read from the parallel passes below.
	files := make(map[string]*yamlfile.File, len(paths))
	loadErrs := make([]error, len(paths))
	loaded := make([]*yamlfile.File, len(paths))
	par.Do(len(paths), func(i int) {
		loaded[i], loadErrs[i] = yamlfile.LoadOrNew(paths[i])
	})
	for i, err := range loadErrs {
		if err != nil {
			return nil, err
		}
		files[paths[i]] = loaded[i]
	}

	// Collected once every file is read and before anything is resolved: the
	// marker may have been typed into a schema file since the last `dbt parse`,
	// so the manifest alone does not know about it; and two declarations that
	// disagree are a question only a person can answer, which asking halfway
	// through would leave half the files written.
	var declared []inherit.Declared
	for _, p := range placements {
		if src := current(p, files); src != nil {
			declared = append(declared, declaredDefinitives(src, p.node)...)
		}
	}
	definitives, err := inherit.BuildDefinitives(graph, resolved.Fold, declared...)
	if err != nil {
		return nil, err
	}
	res.Definitives = definitives

	// Resolving is the expensive part and only reads, so it runs in parallel.
	docs := make([]*inherit.NodeDoc, len(placements))
	par.Do(len(placements), func(i int) {
		p := placements[i]
		var existing inherit.Existing
		if src := current(p, files); src != nil {
			existing = readExisting(src, p.node)
		}
		docs[i] = res.Resolve(p.node, existing)
	})

	// changeAbs is the file each recorded change was made in, by absolute path,
	// so the report can later be reconciled with what was actually written:
	// Change.File is relative to its project, FilesWritten to the repository.
	var changeAbs []string
	record := func(abs string, c Change) {
		rep.Changes = append(rep.Changes, c)
		changeAbs = append(changeAbs, abs)
	}
	for i, p := range placements {
		root := p.node.Project.Root
		fromAbs := ""
		if p.from != "" {
			fromAbs = filepath.Join(root, p.from)
		}
		toAbs := filepath.Join(root, p.to)

		dst := files[toAbs]
		if dst == nil {
			continue
		}
		doc := docs[i]
		rep.Warnings = append(rep.Warnings, doc.Warnings...)

		if fromAbs != "" && fromAbs != toAbs {
			if src := files[fromAbs]; src != nil {
				if entry := entryFor(src, p.node, false); entry != nil {
					carryOver(dst, p.node, entry)
					removeEntry(src, p.node)
					record(toAbs, Change{
						Node:   p.node.UniqueID,
						File:   p.to,
						Detail: "moved from " + p.from,
					})
				}
			}
		}

		wopts := writeOpts{cfg: resolved, configBlock: configBlockFor(p.node)}
		for _, c := range writeDoc(dst, p.node, doc, wopts) {
			record(toAbs, Change{Node: p.node.UniqueID, File: p.to, Detail: c})
		}
	}

	// Save, and drop files organising emptied out.
	type outcome struct {
		rel              string
		written, deleted bool
		err              error
	}
	outcomes := make([]outcome, len(paths))
	par.Do(len(paths), func(i int) {
		path, f := paths[i], files[paths[i]]
		o := outcome{rel: relTo(projects, path)}
		defer func() { outcomes[i] = o }()

		switch {
		case !f.IsEmpty(), !resolved.DeleteEmpty && !f.Created:
			o.written, o.err = f.Save(opts.DryRun)
		case f.Created:
			// Never existed, nothing to write or delete.
		default:
			if !opts.DryRun {
				if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
					o.err = err
					return
				}
			}
			o.deleted = true
		}
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

	// The changes collected above are what the resolver decided to set, which is
	// not the same as what the file ended up saying: setting a value a file
	// already holds changes nothing. Reporting those too meant `-v` listed edits
	// that nothing downstream — git included — could corroborate, so the ones
	// against a file that was not written are dropped here rather than being
	// explained away in the output.
	touched := make(map[string]bool, len(outcomes))
	for i, o := range outcomes {
		if o.written || o.deleted {
			touched[paths[i]] = true
		}
	}
	kept := rep.Changes[:0]
	for i, c := range rep.Changes {
		if touched[changeAbs[i]] {
			kept = append(kept, c)
		}
	}
	rep.Changes = kept
	return rep, nil
}

// configBlockFor decides whether a node's column meta and tags belong inside a
// `config:` block, from the dbt version that produced the manifest, as dbt-osmosis'
// fusion_compat detection does.
func configBlockFor(n *dbt.Node) bool {
	return n.Manifest != nil && n.Manifest.WantsConfigBlock()
}

// loadProjects reads every configured project in parallel.
func loadProjects(cfg *config.Config) ([]*dbt.Project, error) {
	out := make([]*dbt.Project, len(cfg.Projects))
	errs := make([]error, len(cfg.Projects))
	par.Do(len(cfg.Projects), func(i int) {
		ref := cfg.Projects[i]
		// A manifest-only ref has no project directory to read dbt_project.yml from.
		named := ref.Path
		if ref.Manifest != "" {
			named = ref.Manifest
			out[i], errs[i] = dbt.LoadManifestOnly(ref.Name, config.AbsTo(cfg.Dir, ref.Manifest))
		} else {
			out[i], errs[i] = dbt.LoadProject(config.AbsTo(cfg.Dir, ref.Path), ref.Target, !ref.Upstream)
		}
		if errs[i] != nil {
			errs[i] = fmt.Errorf("project %s: %w", named, errs[i])
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
			if slices.Contains(n.Tags, strings.TrimPrefix(s, "tag:")) {
				return true
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

func relTo(projects []*dbt.Project, abs string) string {
	for _, p := range projects {
		if rel := config.Rel(p.Root, abs); rel != abs {
			return filepath.ToSlash(filepath.Join(filepath.Base(p.Root), rel))
		}
	}
	return abs
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

// entryFor locates a node's YAML entry, creating it when create is set.
func entryFor(f *yamlfile.File, n *dbt.Node, create bool) *yaml.Node {
	if n.IsSource() {
		return f.SourceTable(n.SourceName, n.Relation(), create)
	}
	return f.Entry(section(n), n.Name, create)
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
	target := entryFor(dst, n, true)
	for i := 0; i+1 < len(entry.Content); i += 2 {
		key := entry.Content[i].Value
		if key == "name" {
			continue
		}
		yamlfile.MapSet(target, key, entry.Content[i+1])
	}
}
