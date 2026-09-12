package runner

import (
	"context"
	"fmt"

	"github.com/rognerud/dbt-ditto/internal/config"
	"github.com/rognerud/dbt-ditto/internal/dbt"
	"github.com/rognerud/dbt-ditto/internal/inherit"
	"github.com/rognerud/dbt-ditto/internal/sources"
)

// applySources documents the external sources — the raw tables no loaded
// project builds — from the source-provider cache, refreshing it first when
// asked.
//
// The cache is the whole reason this is safe to have on by default. Providers
// are network calls, and `--check` runs in CI, where a flaky API must not fail
// a formatting check and where the runner may hold no warehouse credentials at
// all. So an ordinary run reads a file and spawns nothing; refreshing is a
// thing someone asks for.
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

// sourceProviders turns the configured providers into the runnable form,
// resolving relative commands against the config file's own directory so a
// command path means what the person who wrote it thinks it means.
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

// shadowedSourceWarnings reports a `source:` that points at a relation one of
// the loaded projects builds. It inherits nothing, because a source is a DAG
// root, while the model behind it is documented — so this is almost always a
// declaration that wants to be a cross-project ref instead.
//
// Restricted to the selected nodes so `--select` stays meaningful: a run
// targeting one model should not narrate every source in the repository.
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
