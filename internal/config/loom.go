package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// A project using dbt-loom has already written down where its upstream manifests come
// from, so that list is read from here rather than asked for again in dbt_ditto.yml.
const loomFilename = "dbt_loom.config.yml"

// loomEnv is dbt-loom's own override for the config location.
const loomEnv = "DBT_LOOM_CONFIG"

type loomConfig struct {
	Manifests []loomManifest `yaml:"manifests"`
}

type loomManifest struct {
	Name   string `yaml:"name"`
	Type   string `yaml:"type"`
	Config struct {
		Path string `yaml:"path"`
	} `yaml:"config"`
}

// attachLoomUpstreams appends an upstream ProjectRef for every local manifest a
// project's dbt_loom.config.yml points at.
func (c *Config) attachLoomUpstreams() {
	if c.Loom != nil && !*c.Loom {
		return
	}

	seen := make(map[string]bool, len(c.Projects))
	for _, ref := range c.Projects {
		if ref.Name != "" {
			seen[ref.Name] = true
		}
	}

	// Only the projects configured up front are scanned; refs appended below are
	// manifests, not checkouts, and have no loom config of their own.
	for _, ref := range append([]ProjectRef(nil), c.Projects...) {
		if ref.Path == "" {
			continue
		}
		root := ref.Path
		if !filepath.IsAbs(root) {
			root = filepath.Join(c.Dir, root)
		}
		path, ok := loomConfigPath(root)
		if !ok {
			continue
		}
		manifests, err := readLoomConfig(path)
		if err != nil {
			c.Notes = append(c.Notes, fmt.Sprintf("%s: %v", rel(c.Dir, path), err))
			continue
		}
		for _, m := range manifests {
			if m.Name != "" && seen[m.Name] {
				continue
			}
			// Only `type: file` names something this process can open; the remote types are
			// loom fetching over the network. Said out loud rather than silently skipped.
			if !strings.EqualFold(m.Type, "file") {
				c.Notes = append(c.Notes, fmt.Sprintf(
					"%s: skipping dbt-loom manifest %q (type %q): only `type: file` is read; download the artifact and add it as `manifest:` in dbt_ditto.yml",
					rel(c.Dir, path), m.Name, m.Type))
				continue
			}
			p := os.ExpandEnv(m.Config.Path)
			if p == "" {
				c.Notes = append(c.Notes, fmt.Sprintf(
					"%s: skipping dbt-loom manifest %q: no config.path", rel(c.Dir, path), m.Name))
				continue
			}
			if !filepath.IsAbs(p) {
				// dbt-loom resolves a relative path against the project root the config sits in.
				p = filepath.Join(root, p)
			}
			if _, err := os.Stat(p); err != nil {
				c.Notes = append(c.Notes, fmt.Sprintf(
					"%s: skipping dbt-loom manifest %q: %v", rel(c.Dir, path), m.Name, err))
				continue
			}
			if m.Name != "" {
				seen[m.Name] = true
			}
			c.Projects = append(c.Projects, ProjectRef{
				Name:     m.Name,
				Manifest: p,
				Upstream: true,
			})
		}
	}
}

// loomConfigPath returns the dbt-loom config governing a project root, honouring
func loomConfigPath(root string) (string, bool) {
	p := os.Getenv(loomEnv)
	if p == "" {
		p = loomFilename
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(root, p)
	}
	if _, err := os.Stat(p); err == nil {
		return p, true
	}
	return "", false
}

func readLoomConfig(path string) ([]loomManifest, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var lc loomConfig
	if err := yaml.Unmarshal(raw, &lc); err != nil {
		return nil, fmt.Errorf("parse dbt-loom config: %w", err)
	}
	return lc.Manifests, nil
}

// rel shortens a path for a message, falling back to the absolute path when the
func rel(base, path string) string {
	if r, err := filepath.Rel(base, path); err == nil && !strings.HasPrefix(r, "..") {
		return filepath.ToSlash(r)
	}
	return path
}
