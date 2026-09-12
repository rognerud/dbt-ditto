package dbt

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Project is one dbt project participating in the run: its manifest, its
// catalog if one has been generated, and the on-disk root the YAML files that
// document it are relative to.
type Project struct {
	Name     string
	Root     string // absolute path to the project directory
	Manifest *Manifest
	Catalog  *Catalog

	// Profile is the `profile:` key from dbt_project.yml. dbt-ditto never uses
	// it — it connects to nothing — but a source provider does: it is the entry
	// in profiles.yml holding the credentials dbt itself uses for this project,
	// and passing it through is what lets a provider authenticate exactly the
	// way dbt does instead of being configured a second time.
	Profile string

	// Writable is false for upstream projects loaded only to donate metadata.
	Writable bool

	// ManifestOnly is true when the project arrived as a bare manifest.json
	// with no checkout behind it, which is how a dbt-loom upstream arrives.
	// Nothing here can be read, reviewed or edited beyond the manifest itself.
	ManifestOnly bool

	// pathRules maps a model path prefix (dot-joined fqn parts under `models:`)
	// to the YAML path template configured for it in dbt_project.yml.
	pathRules []pathRule
}

type pathRule struct {
	prefix   []string // fqn segments below the project name, e.g. ["staging"]
	template string
}

// projectYAML is the slice of dbt_project.yml we care about.
type projectYAML struct {
	Name        string         `yaml:"name"`
	ModelPaths  []string       `yaml:"model-paths"`
	Models      map[string]any `yaml:"models"`
	Seeds       map[string]any `yaml:"seeds"`
	Snapshots   map[string]any `yaml:"snapshots"`
	TargetPath  string         `yaml:"target-path"`
	ProfileName string         `yaml:"profile"`
}

// LoadProject reads dbt_project.yml under root and the artifacts under
// targetDir (defaulting to <root>/target). A missing catalog is not an error.
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
	if p.Name == "" {
		p.Name = man.Metadata.ProjectName
	}

	// A missing catalog is normal: `dbt docs generate` may simply not have been
	// run, and inheritance still works from the manifest alone. A catalog that
	// exists but cannot be read is not normal, and silently carrying on without
	// it produces a run that quietly documents almost nothing.
	catalogPath := artifactPath(targetDir, "catalog.json")
	switch cat, err := LoadCatalog(catalogPath); {
	case err == nil:
		p.Catalog = cat
	case !os.IsNotExist(errors.Unwrap(err)) && !os.IsNotExist(err):
		return nil, fmt.Errorf("read %s: %w", catalogPath, err)
	}

	// The keys directly under `models:` are package names, and a node's fqn
	// starts with its package too, so this project's own package is stripped
	// from both sides. Walking the section as-is would record a rule whose
	// prefix begins with the package name and therefore matches nothing.
	for _, section := range []map[string]any{py.Models, py.Seeds, py.Snapshots} {
		if own, ok := section[p.Name].(map[string]any); ok {
			collectPathRules(own, nil, &p.pathRules)
			continue
		}
		collectPathRules(section, nil, &p.pathRules)
	}

	for _, n := range man.Nodes {
		n.Project = p
	}
	for _, n := range man.Sources {
		n.Project = p
	}
	return p, nil
}

// LoadManifestOnly reads an upstream project that arrives as a bare
// manifest.json with no checkout behind it, which is how a dbt-loom upstream is
// configured. The result is never writable: there is no YAML here to edit.
//
// Root is the conventional <root>/target/manifest.json parent, used only to
// print a readable path for a node; nothing is read from it, and it may not
// exist.
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
	if p.Name == "" {
		p.Name = man.Metadata.ProjectName
	}
	for _, n := range man.Nodes {
		n.Project = p
	}
	for _, n := range man.Sources {
		n.Project = p
	}
	return p, nil
}

// artifactPath returns the artifact to read, preferring the plain file and
// falling back to a gzipped one. dbt Cloud serves artifacts gzipped, and a
// checked-in artifact is worth compressing too; both readers handle `.gz`.
func artifactPath(targetDir, name string) string {
	plain := filepath.Join(targetDir, name)
	if _, err := os.Stat(plain); err == nil {
		return plain
	}
	if gz := plain + ".gz"; fileExists(gz) {
		return gz
	}
	return plain
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// pathConfigKeys are the dbt_project.yml keys that carry a YAML path template,
// in precedence order. `dbt-osmosis` is accepted so existing projects work
// unchanged.
var pathConfigKeys = []string{"+dbt-ditto-path", "+dbt-osmosis", "dbt-ditto-path", "dbt-osmosis"}

// collectPathRules walks the nested `models:` tree in dbt_project.yml and
// records every path template it finds along with the fqn prefix it applies to.
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
		if len(k) > 0 && k[0] == '+' {
			continue
		}
		child, ok := v.(map[string]any)
		if !ok {
			continue
		}
		collectPathRules(child, append(prefix, k), out)
	}
}

// PathTemplate returns the YAML path template that applies to a node. Node
// config wins over dbt_project.yml because dbt has already resolved in-model
// config() blocks into it; otherwise the most specific matching fqn prefix wins.
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
		if len(r.prefix) > len(fqn) {
			continue
		}
		match := true
		for i, seg := range r.prefix {
			if fqn[i] != seg {
				match = false
				break
			}
		}
		if match && len(r.prefix) > bestLen {
			best, bestLen = r.template, len(r.prefix)
		}
	}
	return best, best != ""
}
