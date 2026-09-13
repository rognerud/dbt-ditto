package runner

import (
	"testing"

	"github.com/rognerud/dbt-ditto/internal/dbt"
)

// modelWithTemplate builds a node whose dbt_project.yml rule has already been
// resolved onto its config, which is where dbt records a `+dbt-osmosis:` entry.
func modelWithTemplate(template string, n *dbt.Node) *dbt.Node {
	if n.Config == nil {
		n.Config = map[string]any{}
	}
	n.Config["dbt-osmosis"] = template
	n.Project = &dbt.Project{}
	return n
}

func TestTargetSchemaPathTemplates(t *testing.T) {
	base := func() *dbt.Node {
		return &dbt.Node{
			Name:             "stg_orders",
			ResourceType:     "model",
			Schema:           "analytics",
			Database:         "warehouse",
			Alias:            "stg_orders_v2",
			OriginalFilePath: "models/staging/commerce/stg_orders.sql",
			FQN:              []string{"platform", "staging", "commerce", "stg_orders"},
		}
	}

	cases := []struct {
		template string
		want     string
	}{
		// Relative templates land beside the model file.
		{"_{model}.yml", "models/staging/commerce/_stg_orders.yml"},
		{"{model}", "models/staging/commerce/stg_orders.yml"},
		{"_{parent}_models.yml", "models/staging/commerce/_commerce_models.yml"},
		{"{schema}/{model}.yml", "models/staging/commerce/analytics/stg_orders.yml"},
		{"{database}.yml", "models/staging/commerce/warehouse.yml"},
		{"{alias}.yml", "models/staging/commerce/stg_orders_v2.yml"},
		// A leading slash is relative to the model root instead.
		{"/staging/_schema.yml", "models/staging/_schema.yml"},
		{"/_all.yml", "models/_all.yml"},
		// fqn indexing, in both spellings dbt-osmosis accepts.
		{"{fqn[1]}.yml", "models/staging/commerce/staging.yml"},
		{"{node.fqn[-2]}.yml", "models/staging/commerce/commerce.yml"},
		// A .yaml suffix is left alone; anything else gains .yml.
		{"schema.yaml", "models/staging/commerce/schema.yaml"},
	}

	for _, c := range cases {
		n := modelWithTemplate(c.template, base())
		got, ok := TargetSchemaPath(n)
		if !ok {
			t.Errorf("%s: no path produced", c.template)
			continue
		}
		if got != c.want {
			t.Errorf("%s: path = %q, want %q", c.template, got, c.want)
		}
	}
}

func TestTargetSchemaPathIgnoresSourcesAndUnconfiguredModels(t *testing.T) {
	src := modelWithTemplate("_{model}.yml", &dbt.Node{
		ResourceType: "source", Name: "raw_orders",
		OriginalFilePath: "models/staging/_sources.yml",
	})
	if _, ok := TargetSchemaPath(src); ok {
		t.Error("sources are defined by hand and must not be moved")
	}

	plain := &dbt.Node{Name: "m", OriginalFilePath: "models/m.sql", Project: &dbt.Project{}}
	if _, ok := TargetSchemaPath(plain); ok {
		t.Error("a model with no path rule must stay where it is documented")
	}
}

func TestTargetSchemaPathHandlesAnOutOfRangeFqnIndex(t *testing.T) {
	n := modelWithTemplate("{fqn[9]}.yml", &dbt.Node{
		Name: "m", OriginalFilePath: "models/m.sql", FQN: []string{"platform", "m"},
	})
	got, ok := TargetSchemaPath(n)
	if !ok || got != "models/.yml" {
		t.Errorf("path = %q (ok=%v); an out-of-range index expands to nothing rather than crashing", got, ok)
	}
}

func TestSelectorMatching(t *testing.T) {
	n := &dbt.Node{
		UniqueID:         "model.platform.stg_orders",
		Name:             "stg_orders",
		Tags:             []string{"nightly", "core"},
		OriginalFilePath: "models/staging/stg_orders.sql",
		FQN:              []string{"platform", "staging", "stg_orders"},
	}

	cases := []struct {
		selectors []string
		want      bool
	}{
		{nil, true},
		{[]string{"stg_orders"}, true},
		{[]string{"stg_*"}, true},
		{[]string{"dim_*"}, false},
		{[]string{"tag:nightly"}, true},
		{[]string{"tag:weekly"}, false},
		{[]string{"path:models/staging"}, true},
		{[]string{"path:models/marts"}, false},
		{[]string{"model.platform.stg_orders"}, true},
		{[]string{"platform.staging.*"}, true},
		{[]string{"dim_*", "tag:nightly"}, true},
	}
	for _, c := range cases {
		if got := matches(n, c.selectors); got != c.want {
			t.Errorf("matches(%v) = %v, want %v", c.selectors, got, c.want)
		}
	}
}

func TestDefaultSchemaPath(t *testing.T) {
	model := &dbt.Node{Name: "stg_orders", OriginalFilePath: "models/staging/stg_orders.sql"}
	if got := defaultSchemaPath(model); got != "models/staging/_stg_orders.yml" {
		t.Errorf("path = %q, want a file beside the model", got)
	}

	src := &dbt.Node{ResourceType: "source", OriginalFilePath: "models/staging/_sources.yml"}
	if got := defaultSchemaPath(src); got != "models/staging/_sources.yml" {
		t.Errorf("path = %q, want a source to stay in its own file", got)
	}
}
