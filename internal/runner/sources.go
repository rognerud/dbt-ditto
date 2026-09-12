package runner

import (
	"context"
	"fmt"

	"github.com/rognerud/dbt-ditto/internal/config"
	"github.com/rognerud/dbt-ditto/internal/dbt"
	"github.com/rognerud/dbt-ditto/internal/inherit"
	"github.com/rognerud/dbt-ditto/internal/sources"
)

// applySources documents the external sources from the provider cache, refreshing it
// first when asked.
func applySources(cfg *config.Config, resolved config.Resolved, graph *inherit.Graph, opts Options) ([]string, error) {
	external, _ := graph.ClassifySources()
	if len(external) == 0 {
		return nil, nil
	}

	cachePath := cfg.SourceCachePath()
	var set *sources.Set
	var notes []string

	switch {
	case opts.RefreshSources:
		providers := sourceProviders(cfg)
		if len(providers) == 0 {
			return nil, fmt.Errorf("--refresh-sources: no providers configured (sources.providers in %s)", cfg.Dir)
		}
		projects, want := sources.RequestFor(external, sources.Target(opts.Target))
		set = sources.Fetch(context.Background(), providers, projects, want)
		if resolved.SourcesStrict && len(set.Warnings) > 0 {
			return nil, fmt.Errorf("source providers: %s", set.Warnings[0])
		}
		if err := sources.Save(cachePath, set); err != nil {
			return nil, fmt.Errorf("write source cache: %w", err)
		}
		notes = append(notes, fmt.Sprintf("refreshed %s: %s",
			cachePath, sources.Describe(set, len(want))))
	default:
		var err error
		set, err = sources.Load(cachePath)
		if err != nil {
			return nil, err
		}
	}

	notes = append(notes, set.Warnings...)
	notes = append(notes, sources.Apply(set, external, resolved)...)
	if resolved.SourcesStrict && len(set.Warnings) > 0 {
		return nil, fmt.Errorf("source providers: %s", set.Warnings[0])
	}
	return notes, nil
}

// sourceProviders turns the configured providers into runnable form, resolving
// relative commands against the config file's own directory.
func sourceProviders(cfg *config.Config) []sources.Provider {
	out := make([]sources.Provider, 0, len(cfg.Sources.Providers))
	for _, p := range cfg.Sources.Providers {
		if p.Command == "" {
			continue
		}
		out = append(out, sources.Provider{
			Command:  p.Command,
			Database: p.Match.Database,
			Schema:   p.Match.Schema,
			Dir:      cfg.Dir,
		})
	}
	return out
}

// shadowedSourceWarnings reports a `source:` pointing at a relation a loaded project
// builds: a DAG root inheriting nothing, standing in front of a documented model, which
// almost always wants to be a cross-project ref.
func shadowedSourceWarnings(graph *inherit.Graph, targets []*dbt.Node) []inherit.Warning {
	_, shadowed := graph.ClassifySources()
	if len(shadowed) == 0 {
		return nil
	}
	selected := make(map[string]bool, len(targets))
	for _, n := range targets {
		selected[n.UniqueID] = true
	}
	var out []inherit.Warning
	for _, s := range shadowed {
		if !s.Mistake() || !selected[s.Source.UniqueID] {
			continue
		}
		out = append(out, inherit.Warning{
			Node: s.Source.UniqueID,
			Kind: inherit.WarnShadowedSource,
			Detail: fmt.Sprintf("declared as a source but built by %s; a ref would inherit its documentation",
				s.By.UniqueID),
		})
	}
	return out
}
