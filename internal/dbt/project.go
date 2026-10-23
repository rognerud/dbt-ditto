package dbt

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Project is one dbt project in the run: its manifest, its catalog if one has
// been generated, and the on-disk root its YAML files are relative to.
type Project struct {
	Name     string
	Root     string // absolute path to the project directory
	Manifest *Manifest
	Catalog  *Catalog

	// Profile is the `profile:` key from dbt_project.yml.
	Profile string

	// Writable is false for upstream projects loaded only to donate metadata.
	Writable bool

	// ManifestOnly is true when the project arrived as a bare manifest.json with no
	// checkout behind it, which is how a dbt-loom upstream arrives.
	ManifestOnly bool

	// pathRules maps a model path prefix (dot-joined fqn parts under `models:`)
	pathRules []pathRule

	// FolderTagRules are the `+tags:` declared in the same tree, by the same
	// prefixes. Exported because what a folder declares is an input a caller may
	// want to see without re-reading dbt_project.yml.
	FolderTagRules []FolderTagRule

	// FolderMetaRules are the `+meta:` declared in that tree, read for the same
	// reason: a folder's meta is a claim about every node below it.
	FolderMetaRules []FolderMetaRule
}

type pathRule struct {
	prefix   []string // fqn segments below the project name, e.g. ["staging"]
	template string
}

// FolderTagRule is one folder's `+tags:` out of dbt_project.yml.
type FolderTagRule struct {
	// Kind is the resource_type the section it was declared in configures, so the
	// `models:` tree cannot tag a seed that happens to sit at the same fqn depth.
	Kind string
	// Prefix is the fqn segments below the project name the rule was declared at;
	// empty means every node of that kind.
	Prefix []string
	Tags   []string
}

// FolderMetaRule is one `+meta:` block declared in dbt_project.yml, against the
// fqn prefix it governs.
type FolderMetaRule struct {
	Kind   string
	Prefix []string
	Meta   map[string]any
}

// projectYAML is the slice of dbt_project.yml we care about.
type projectYAML struct {
	Name        string         `yaml:"name"`
	Models      map[string]any `yaml:"models"`
	Seeds       map[string]any `yaml:"seeds"`
	Snapshots   map[string]any `yaml:"snapshots"`
	Sources     map[string]any `yaml:"sources"`
	ProfileName string         `yaml:"profile"`
}

// LoadProject reads dbt_project.yml under root and the artifacts under
func LoadProject(root, targetDir string, writable bool) (*Project, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}

	raw, err := os.ReadFile(filepath.Join(abs, "dbt_project.yml"))
	if err != nil {
		return nil, fmt.Errorf("read dbt_project.yml: %w", err)
	}
	var py projectYAML
	if err := yaml.Unmarshal(raw, &py); err != nil {
		return nil, fmt.Errorf("parse dbt_project.yml: %w", err)
	}

	if targetDir == "" {
		targetDir = filepath.Join(abs, "target")
	} else if !filepath.IsAbs(targetDir) {
		targetDir = filepath.Join(abs, targetDir)
	}

	man, err := LoadManifest(artifactPath(targetDir, "manifest.json"))
	if err != nil {
		return nil, err
	}

	p := &Project{
		Name:     py.Name,
		Root:     abs,
		Manifest: man,
		Writable: writable,
		Profile:  py.ProfileName,
	}
	p.claim()

	// A missing catalog is normal: `dbt docs generate` may not have been run, and
	// inheritance still works from the manifest.
	catalogPath := artifactPath(targetDir, "catalog.json")
	switch cat, err := LoadCatalog(catalogPath); {
	case err == nil:
		p.Catalog = cat
	case !os.IsNotExist(errors.Unwrap(err)) && !os.IsNotExist(err):
		return nil, fmt.Errorf("read %s: %w", catalogPath, err)
	}

	// The keys under `models:` are package names, and a node's fqn starts with its
	// package too, so this project's own package is stripped from both sides.
	for _, s := range []struct {
		kind    string
		section map[string]any
	}{
		{"model", py.Models}, {"seed", py.Seeds},
		{"snapshot", py.Snapshots}, {"source", py.Sources},
	} {
		section := s.section
		if own, ok := section[p.Name].(map[string]any); ok {
			section = own
		}
		// Path templates are not read from `sources:`: a source is documented where
		// its `sources:` block already lives, and is never moved.
		if s.kind != "source" {
			collectPathRules(section, nil, &p.pathRules)
		}
		collectTagRules(s.kind, section, nil, &p.FolderTagRules)
		collectMetaRules(s.kind, section, nil, &p.FolderMetaRules)
	}
	SortFolderTagRules(p.FolderTagRules)
	SortFolderMetaRules(p.FolderMetaRules)
	return p, nil
}

// claim names the project after its manifest when dbt_project.yml did not, and
// points every node of that manifest back at it.
func (p *Project) claim() {
	if p.Name == "" {
		p.Name = p.Manifest.Metadata.ProjectName
	}
	for _, m := range []map[string]*Node{p.Manifest.Nodes, p.Manifest.Sources} {
		for _, n := range m {
			n.Project = p
		}
	}
}

// LoadManifestOnly reads an upstream that arrives as a bare manifest.json, as a
// dbt-loom upstream does.
func LoadManifestOnly(name, manifestPath string) (*Project, error) {
	abs, err := filepath.Abs(manifestPath)
	if err != nil {
		return nil, err
	}
	man, err := LoadManifest(abs)
	if err != nil {
		return nil, err
	}
	p := &Project{
		Name:         name,
		Root:         filepath.Dir(filepath.Dir(abs)),
		Manifest:     man,
		Writable:     false,
		ManifestOnly: true,
	}
	p.claim()
	return p, nil
}

// artifactPath prefers the plain file and falls back to a gzipped one.
func artifactPath(targetDir, name string) string {
	plain := filepath.Join(targetDir, name)
	if gz := plain + ".gz"; !fileExists(plain) && fileExists(gz) {
		return gz
	}
	return plain
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// pathConfigKeys are the dbt_project.yml keys carrying a YAML path template, in
// precedence order. `dbt-osmosis` is accepted so existing projects work.
var pathConfigKeys = []string{"+dbt-ditto-path", "+dbt-osmosis", "dbt-ditto-path", "dbt-osmosis"}

// collectPathRules walks the nested `models:` tree in dbt_project.yml and
func collectPathRules(section map[string]any, prefix []string, out *[]pathRule) {
	if section == nil {
		return
	}
	for _, key := range pathConfigKeys {
		if v, ok := section[key]; ok {
			if s, ok := v.(string); ok && s != "" {
				*out = append(*out, pathRule{prefix: append([]string(nil), prefix...), template: s})
				break
			}
		}
	}
	for k, v := range section {
		if k != "" && k[0] == '+' {
			continue
		}
		child, ok := v.(map[string]any)
		if !ok {
			continue
		}
		collectPathRules(child, append(prefix, k), out)
	}
}

// tagConfigKeys carry a folder's tags. dbt itself only reads `+tags`; the
// unprefixed spelling is accepted because dbt_project.yml allows it for any
// config key, and a project that uses it means the same thing by it.
var tagConfigKeys = []string{"+tags", "tags"}

// collectTagRules walks the nested `models:` tree collecting every `+tags:`
// against the fqn prefix it was declared at.
func collectTagRules(kind string, section map[string]any, prefix []string, out *[]FolderTagRule) {
	if section == nil {
		return
	}
	for _, key := range tagConfigKeys {
		v, ok := section[key]
		if !ok {
			continue
		}
		if tags := decodeTags(v); len(tags) > 0 {
			*out = append(*out, FolderTagRule{Kind: kind, Prefix: append([]string(nil), prefix...), Tags: tags})
		}
		break
	}
	for k, v := range section {
		if k != "" && k[0] == '+' {
			continue
		}
		child, ok := v.(map[string]any)
		if !ok {
			continue
		}
		collectTagRules(kind, child, append(prefix, k), out)
	}
}

// decodeTags accepts dbt's two spellings: a single tag, or a list of them.
func decodeTags(v any) []string {
	switch t := v.(type) {
	case string:
		if t == "" {
			return nil
		}
		return []string{t}
	case []any:
		out := make([]string, 0, len(t))
		for _, e := range t {
			if s, ok := e.(string); ok && s != "" {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}

// metaConfigKeys carry a folder's meta, with the same two spellings as its
// tags: dbt reads `+meta`, and a project that writes it unprefixed means the
// same thing by it.
var metaConfigKeys = []string{"+meta", "meta"}

// collectMetaRules walks the nested tree collecting every `+meta:` against the
// fqn prefix it was declared at, as collectTagRules does for `+tags:`.
func collectMetaRules(kind string, section map[string]any, prefix []string, out *[]FolderMetaRule) {
	if section == nil {
		return
	}
	for _, key := range metaConfigKeys {
		v, ok := section[key]
		if !ok {
			continue
		}
		if meta, ok := v.(map[string]any); ok && len(meta) > 0 {
			*out = append(*out, FolderMetaRule{
				Kind:   kind,
				Prefix: append([]string(nil), prefix...),
				Meta:   meta,
			})
		}
		break
	}
	for k, v := range section {
		if k != "" && k[0] == '+' {
			continue
		}
		child, ok := v.(map[string]any)
		if !ok {
			continue
		}
		collectMetaRules(kind, child, append(prefix, k), out)
	}
}

// SortFolderMetaRules orders the rules shallowest first, so a nearer folder's
// value is applied after — and therefore wins over — the one it narrowed.
func SortFolderMetaRules(rules []FolderMetaRule) {
	sort.SliceStable(rules, func(i, j int) bool {
		if rules[i].Kind != rules[j].Kind {
			return rules[i].Kind < rules[j].Kind
		}
		if len(rules[i].Prefix) != len(rules[j].Prefix) {
			return len(rules[i].Prefix) < len(rules[j].Prefix)
		}
		return strings.Join(rules[i].Prefix, ".") < strings.Join(rules[j].Prefix, ".")
	})
}

// FolderMeta returns the `+meta:` every folder above a node declares, merged
// outermost first so the nearest folder's value for a key wins. Unlike tags,
// which accumulate, a meta key has one value and dbt lets the deeper
// declaration replace it.
func (p *Project) FolderMeta(n *Node) map[string]any {
	if len(p.FolderMetaRules) == 0 {
		return nil
	}
	fqn := n.FQN
	if len(fqn) > 0 {
		fqn = fqn[1:]
	}
	var out map[string]any
	for _, r := range p.FolderMetaRules {
		if r.Kind != n.ResourceType || len(r.Prefix) > len(fqn) {
			continue
		}
		if !slices.Equal(r.Prefix, fqn[:len(r.Prefix)]) {
			continue
		}
		if out == nil {
			out = make(map[string]any, len(r.Meta))
		}
		for k, v := range r.Meta {
			out[k] = v
		}
	}
	return out
}

// SortFolderTagRules puts the shallowest folder first, so FolderTags returns a
// project-wide tag before the tag of the folder that narrowed it, and the order
// written into a file does not depend on map iteration.
func SortFolderTagRules(rules []FolderTagRule) {
	sort.SliceStable(rules, func(i, j int) bool {
		if rules[i].Kind != rules[j].Kind {
			return rules[i].Kind < rules[j].Kind
		}
		if len(rules[i].Prefix) != len(rules[j].Prefix) {
			return len(rules[i].Prefix) < len(rules[j].Prefix)
		}
		return strings.Join(rules[i].Prefix, ".") < strings.Join(rules[j].Prefix, ".")
	})
}

// FolderTags returns the `+tags:` every folder above a node declares, outermost
// first. dbt accumulates these rather than letting the nearest one win, so this
// does too: a node under `marts/finance` carries the tags of `marts` as well.
func (p *Project) FolderTags(n *Node) []string {
	if len(p.FolderTagRules) == 0 {
		return nil
	}
	// fqn[0] is the package name; the rules are keyed below it.
	fqn := n.FQN
	if len(fqn) > 0 {
		fqn = fqn[1:]
	}
	var out []string
	for _, r := range p.FolderTagRules {
		if r.Kind != n.ResourceType || len(r.Prefix) > len(fqn) {
			continue
		}
		if slices.Equal(r.Prefix, fqn[:len(r.Prefix)]) {
			out = UnionTags(out, r.Tags)
		}
	}
	return out
}

// PathTemplate returns the YAML path template that applies to a node.
func (p *Project) PathTemplate(n *Node) (string, bool) {
	for _, key := range []string{"dbt-ditto-path", "dbt-osmosis"} {
		if s, ok := n.ConfigString(key); ok && s != "" {
			return s, true
		}
	}

	// fqn[0] is the package name; the rules are keyed below it.
	fqn := n.FQN
	if len(fqn) > 0 {
		fqn = fqn[1:]
	}

	best := ""
	bestLen := -1
	for _, r := range p.pathRules {
		if len(r.prefix) > len(fqn) || len(r.prefix) <= bestLen {
			continue
		}
		if slices.Equal(r.prefix, fqn[:len(r.prefix)]) {
			best, bestLen = r.template, len(r.prefix)
		}
	}
	return best, best != ""
}
