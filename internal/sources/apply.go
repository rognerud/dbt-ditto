package sources

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/rognerud/dbt-ditto/internal/config"
	"github.com/rognerud/dbt-ditto/internal/dbt"
)

// Apply folds provider documentation into the loaded projects, before the graph is
// built, and returns the warnings worth printing.
func Apply(set *Set, sources []*dbt.Node, cfg config.Resolved) []string {
	if set.Len() == 0 || len(sources) == 0 {
		return nil
	}
	var warnings []string
	for _, n := range sources {
		doc, ok := set.Lookup(n.UniqueID)
		if !ok {
			continue
		}
		warnings = append(warnings, applyOne(n, doc, cfg)...)
	}
	sort.Strings(warnings)
	return warnings
}

func applyOne(n *dbt.Node, doc *Doc, cfg config.Resolved) []string {
	var warnings []string
	labels := cfg.SourceLabels

	// A description written by hand always wins.
	if n.Description == "" {
		n.Description = doc.Description
	}
	if kept, dropped := filterLabels(doc.Labels, labels); labels.Routed() {
		warnings = append(warnings, dropped...)
		applyNodeLabels(n, kept, labels)
	}

	if len(doc.Columns) == 0 {
		return warnings
	}

	// The catalog entry: what the relation really contains.
	entry := &dbt.CatalogNode{
		Metadata: dbt.CatalogMetadata{
			Name:     n.Relation(),
			Schema:   n.Schema,
			Database: n.Database,
		},
		Columns: make(map[string]dbt.CatalogColumn, len(doc.Columns)),
	}
	for i, c := range doc.Columns {
		if c.Name == "" {
			continue
		}
		index := c.Index
		if index == 0 {
			// No opinion about ordinals: fall back to the listed order, since a catalog
			// with every index at zero would sort alphabetically.
			index = i + 1
		}
		entry.Columns[c.Name] = dbt.CatalogColumn{
			Name:    c.Name,
			Type:    c.DataType,
			Index:   index,
			Comment: c.Description,
		}
		warnings = append(warnings, applyColumn(n, c, cfg)...)
	}
	attachCatalog(n, entry)
	return warnings
}

// applyColumn writes the metadata a catalog cannot carry onto the source's own
func applyColumn(n *dbt.Node, c ColumnDoc, cfg config.Resolved) []string {
	labels := cfg.SourceLabels
	kept, dropped := filterLabels(c.Labels, labels)

	col := n.Column(c.Name, cfg.CaseInsensitive)
	if col == nil {
		// An entry is created so inheritance downstream can see the column at all; the
		// description stays on the catalog side, where precedence already ranks a
		// warehouse comment below hand-written text.
		col = &dbt.Column{Name: c.Name, DataType: c.DataType}
		if n.Columns == nil {
			n.Columns = map[string]*dbt.Column{}
		}
		n.Columns[c.Name] = col
		n.InvalidateColumnIndex()
	}

	// Written onto the manifest column rather than left as a catalog comment:
	// `columns.comments` defaults to "new", which would skip a column the source YAML
	// already lists — wrong for an external source, since nothing upstream exists.
	if col.Description == "" {
		col.Description = c.Description
	}

	for k, v := range c.Meta {
		setMeta(col, k, v)
	}
	col.Tags = dbt.UnionTags(col.Tags, c.Tags)

	// Extra keys are carried only when the project asked for them by name, so a
	// provider reporting a policy tag into a project that never configured
	// `inheritance.extra_keys` writes nothing.
	for _, key := range cfg.ExtraKeys {
		v, ok := c.Extra[key]
		if !ok || v == nil {
			continue
		}
		if col.Extra == nil {
			col.Extra = map[string]any{}
		}
		if _, taken := col.Extra[key]; !taken {
			col.Extra[key] = v
		}
	}

	if labels.Routed() {
		// Into the manifest column, so a label travels downstream exactly as a
		// description does. `propagate.column` stops that via the skip list.
		applyColumnLabels(col, kept, labels)
	}
	return dropped
}

// attachCatalog files an entry under the node's unique_id, creating the project's
// catalog if `dbt docs generate` was never run.
func attachCatalog(n *dbt.Node, entry *dbt.CatalogNode) {
	if n.Project == nil {
		return
	}
	if n.Project.Catalog == nil {
		n.Project.Catalog = &dbt.Catalog{
			Nodes:   map[string]*dbt.CatalogNode{},
			Sources: map[string]*dbt.CatalogNode{},
		}
	}
	c := n.Project.Catalog
	if c.Sources == nil {
		c.Sources = map[string]*dbt.CatalogNode{}
	}
	if _, exists := c.Sources[n.UniqueID]; exists {
		return
	}
	if _, exists := c.Nodes[n.UniqueID]; exists {
		return
	}
	c.Sources[n.UniqueID] = entry
	c.Invalidate()
}

// applyNodeLabels routes a relation's own labels onto the source entry.
func applyNodeLabels(n *dbt.Node, labels map[string]string, l config.ResolvedLabels) {
	if len(labels) == 0 {
		return
	}
	if l.ToMeta() {
		if n.Meta == nil {
			n.Meta = map[string]any{}
		}
		if l.MetaKey == "" {
			for _, k := range sortedKeys(labels) {
				if _, taken := n.Meta[k]; !taken {
					n.Meta[k] = labels[k]
				}
			}
		} else if _, taken := n.Meta[l.MetaKey]; !taken {
			n.Meta[l.MetaKey] = labelMap(labels)
		}
	}
	if l.ToTags() {
		n.Tags = dbt.UnionTags(n.Tags, RenderTags(labels, l))
	}
}

// applyColumnLabels routes a column's labels into its meta and tags.
func applyColumnLabels(col *dbt.Column, labels map[string]string, l config.ResolvedLabels) {
	if len(labels) == 0 {
		return
	}
	if l.ToMeta() {
		if l.MetaKey == "" {
			for _, k := range sortedKeys(labels) {
				setMetaIfAbsent(col, k, labels[k])
			}
		} else {
			setMetaIfAbsent(col, l.MetaKey, labelMap(labels))
		}
	}
	if l.ToTags() {
		col.Tags = dbt.UnionTags(col.Tags, RenderTags(labels, l))
	}
}

// RenderTags turns label pairs into dbt tags, in key order so a run is stable.
func RenderTags(labels map[string]string, l config.ResolvedLabels) []string {
	out := make([]string, 0, len(labels))
	for _, k := range sortedKeys(labels) {
		v := labels[k]
		if v == "" {
			// BigQuery permits a label with no value, and `owner:` is not a useful tag.
			out = append(out, k)
			continue
		}
		r := strings.NewReplacer("{key}", k, "{value}", v)
		out = append(out, r.Replace(l.TagFormat))
	}
	return out
}

// LabelMap renders label pairs as the ordered map written under the meta key.
func labelMap(labels map[string]string) *dbt.OrderedMap {
	m := dbt.NewOrderedMap()
	for _, k := range sortedKeys(labels) {
		m.Set(k, labels[k])
	}
	return m
}

// filterLabels applies the include and exclude patterns, and reports keys
// dropped for an unusable name rather than by configuration — the provider's
// bug, and worth saying.
func filterLabels(labels map[string]string, l config.ResolvedLabels) (map[string]string, []string) {
	if len(labels) == 0 {
		return nil, nil
	}
	var bad []string
	out := make(map[string]string, len(labels))
	for k, v := range labels {
		if k == "" {
			bad = append(bad, "source provider reported a label with an empty key")
			continue
		}
		if len(l.Include) > 0 && !anyMatch(l.Include, k) {
			continue
		}
		if anyMatch(l.Exclude, k) {
			continue
		}
		out[k] = v
	}
	if len(out) == 0 {
		return nil, bad
	}
	return out, bad
}

func anyMatch(patterns []string, key string) bool {
	for _, p := range patterns {
		if ok, err := path.Match(strings.ToLower(p), strings.ToLower(key)); err == nil && ok {
			return true
		}
	}
	return false
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func setMeta(col *dbt.Column, key string, value any) {
	if col.Meta == nil {
		col.Meta = dbt.NewOrderedMap()
	}
	col.Meta.Set(key, value)
}

func setMetaIfAbsent(col *dbt.Column, key string, value any) {
	if m := col.EffectiveMeta(); m != nil {
		if _, taken := m.Get(key); taken {
			return
		}
	}
	setMeta(col, key, value)
}

// RequestFor turns the external sources into the request a provider receives,
// with the dbt projects they belong to so it can authenticate as dbt does.
func RequestFor(nodes []*dbt.Node, target string) ([]RequestProject, []RequestSource) {
	sources := make([]RequestSource, 0, len(nodes))
	projects := make([]RequestProject, 0, 2)
	seen := map[string]bool{}

	for _, n := range nodes {
		project := ""
		if n.Project != nil {
			project = n.Project.Name
			if !seen[project] {
				seen[project] = true
				projects = append(projects, RequestProject{
					Name:        project,
					Root:        n.Project.Root,
					Profile:     profileName(n.Project),
					Target:      target,
					ProfilesDir: ProfilesDir(n.Project.Root),
				})
			}
		}
		sources = append(sources, RequestSource{
			UniqueID:   n.UniqueID,
			Database:   n.Database,
			Schema:     n.Schema,
			Identifier: n.Relation(),
			SourceName: n.SourceName,
			Name:       n.Name,
			Project:    project,
		})
	}
	sort.Slice(projects, func(i, j int) bool { return projects[i].Name < projects[j].Name })
	return projects, sources
}

// profileName falls back to the project name, as dbt does when dbt_project.yml
// has no `profile:` key.
func profileName(p *dbt.Project) string {
	if p.Profile != "" {
		return p.Profile
	}
	return p.Name
}

// ProfilesDir reproduces dbt's own search for profiles.yml: DBT_PROFILES_DIR, then the
// project directory, then ~/.dbt.
func ProfilesDir(projectRoot string) string {
	if dir := os.Getenv("DBT_PROFILES_DIR"); dir != "" {
		return dir
	}
	if projectRoot != "" {
		if _, err := os.Stat(filepath.Join(projectRoot, "profiles.yml")); err == nil {
			return projectRoot
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".dbt")
}

// Target follows dbt's precedence: an explicit --target, then DBT_TARGET, then
// whatever profiles.yml calls default — the provider's to discover, so an empty
// string is passed through rather than guessed at.
func Target(explicit string) string {
	if explicit != "" {
		return explicit
	}
	return os.Getenv("DBT_TARGET")
}

// Describe summarises a fetch for the run's output.
func Describe(set *Set, asked int) string {
	return fmt.Sprintf("%d of %d external sources documented", set.Len(), asked)
}
