package runner_test

import (
	"testing"

	"github.com/rognerud/dbt-ditto/internal/config"
)

// sensitivityRule is the ladder every case here is about: the four values a
// data-sensitivity label takes, weakest first.
func sensitivityRule(tweak ...func(*config.TagRule)) config.TagRule {
	r := config.TagRule{
		Key:    "data-sensitivity",
		Values: []string{"public", "internal", "confidential", "sensitive"},
	}
	for _, f := range tweak {
		f(&r)
	}
	return r
}

// withSensitivity pre-configures the `off` run with the rule, for the settings
// that only mean anything once a rule exists.
func withSensitivity(tweak ...func(*config.TagRule)) func(*config.Config) {
	return func(c *config.Config) { c.Tags.Rules = []config.TagRule{sensitivityRule(tweak...)} }
}

// disagreeingParents puts a different sensitivity on each of the two seeds the
// staging model reads, which is the situation a ladder exists to settle.
func disagreeingParents(t *testing.T, s *stage) {
	t.Helper()
	s.col(t, "seed.demo.raw", "id")["tags"] = []string{"core", "data-sensitivity:public"}
	s.col(t, "seed.demo.raw_alt", "id")["tags"] = []string{"data-sensitivity:confidential"}
}

// inStagingFolder moves the model into a folder that declares a sensitivity, so
// `tags.folders` has something to read.
func inStagingFolder(t *testing.T, s *stage) {
	t.Helper()
	s.node(t, "model.demo.stg")["fqn"] = []string{"demo", "staging", "stg"}
	s.projectYAML += "\nmodels:\n  demo:\n    staging:\n" +
		"      +tags: [\"nightly\", \"data-sensitivity:confidential\"]\n"
}

func tagSettingCases() []settingCase {
	return []settingCase{
		{
			key: "tags.folders",
			claim: "reads dbt_project.yml's `+tags:` and treats what a folder declares as " +
				"canonical for the nodes below it",
			build: stageBuildCfg(inStagingFolder,
				withSensitivity(func(r *config.TagRule) { r.Default = ptrTo("public") })),
			set: func(_ *testing.T, c *config.Config, _ string) {
				c.Tags.Folders = ptrTo(config.FolderTagsRules)
			},
			off: inCol("ID", "data-sensitivity:public",
				"with the folders unread the rule's own default should decide"),
			on: inCol("ID", "data-sensitivity:confidential",
				"the folder's declaration did not reach the column"),
		},
		{
			key:   "tags.rules[].key",
			claim: "names the label key the rule decides",
			build: stageBuildCfg(nil,
				withSensitivity(func(r *config.TagRule) { r.Default = ptrTo("public") })),
			set: func(_ *testing.T, c *config.Config, _ string) { c.Tags.Rules[0].Key = "classification" },
			off: inCol("ID", "data-sensitivity:public", "the rule did not write its own key"),
			on:  inCol("ID", "classification:public", "the renamed key was not the one written"),
		},
		{
			key:   "tags.rules[].values",
			claim: "is the vocabulary, weakest first, and so decides which value is the stronger",
			build: stageBuildCfg(disagreeingParents, withSensitivity()),
			set: func(_ *testing.T, c *config.Config, _ string) {
				// The same four values, read the other way round: `public` is now the
				// strongest thing anybody can say about a column.
				c.Tags.Rules[0].Values = []string{"sensitive", "confidential", "internal", "public"}
			},
			off: inCol("ID", "data-sensitivity:confidential",
				"the stronger of the two parents did not win"),
			on: inCol("ID", "data-sensitivity:public",
				"reordering the vocabulary did not reorder the strengths"),
		},
		{
			key:   "tags.rules[].resolve",
			claim: "picks between the values the ancestors carry: strongest, weakest or first",
			build: stageBuildCfg(disagreeingParents, withSensitivity()),
			set: func(_ *testing.T, c *config.Config, _ string) {
				c.Tags.Rules[0].Resolve = ptrTo(config.ResolveWeakest)
			},
			off: inCol("ID", "data-sensitivity:confidential", "strongest is not the default"),
			on:  inCol("ID", "data-sensitivity:public", "weakest did not take the least strict value"),
		},
		{
			key: "tags.rules[].inherit",
			claim: "decides whether a branch stops at the nearest ancestor that declares the " +
				"key, or every ancestor is read",
			// The seed classifies the column, the staging model alters it by hand,
			// and the question is what the model below the alteration gets.
			build: stageBuildCfg(func(t *testing.T, s *stage) {
				t.Helper()
				s.col(t, "seed.demo.raw", "id")["tags"] = []string{"data-sensitivity:sensitive"}
				s.col(t, "model.demo.stg", "ID")["tags"] = []string{"data-sensitivity:internal"}
				s.files["models/_stg.yml"] = "version: 2\nmodels:\n  - name: stg\n    columns:\n" +
					"      - name: ID\n        tags:\n          - data-sensitivity:internal\n" +
					"      - name: legacy\n        description: Written by hand, and gone from the warehouse.\n"
				// A model below the alteration, which is the one the setting is about.
				s.nodes["model.demo.dim"] = modelNode("dim", "models/_dim.yml",
					[]string{"model.demo.stg"}, column("ID", ""))
				s.files["models/_dim.yml"] = "version: 2\nmodels:\n  - name: dim\n    columns:\n" +
					"      - name: ID\n"
			}, withSensitivity()),
			set: func(_ *testing.T, c *config.Config, _ string) {
				c.Tags.Rules[0].Inherit = ptrTo(config.RuleInheritAll)
			},
			off: inFile("models/_dim.yml", "data-sensitivity:internal",
				"cut should carry the alteration made above, not the source it came from"),
			on: inFile("models/_dim.yml", "data-sensitivity:sensitive",
				"inherit: all should read past the alteration to the source"),
		},
		{
			key:   "tags.rules[].default",
			claim: "is written when no ancestor carries the key at all",
			build: stageBuildCfg(nil, withSensitivity()),
			set: func(_ *testing.T, c *config.Config, _ string) {
				c.Tags.Rules[0].Default = ptrTo("internal")
			},
			off: notInCol("ID", "data-sensitivity",
				"a rule with no default invented a value nothing upstream said"),
			on: inCol("ID", "data-sensitivity:internal", "the default was not written"),
		},
		{
			key:   "tags.rules[].write",
			claim: "routes the resolved value to tags, to meta, or to both",
			build: stageBuildCfg(nil,
				withSensitivity(func(r *config.TagRule) { r.Default = ptrTo("internal") })),
			set: func(_ *testing.T, c *config.Config, _ string) {
				c.Tags.Rules[0].Write = ptrTo(config.TagRuleWriteMeta)
			},
			off: inCol("ID", "data-sensitivity:internal", "the default route is a tag"),
			on: both(
				notInCol("ID", "data-sensitivity:internal", "write: meta still wrote a tag"),
				inCol("ID", "data-sensitivity: internal", "write: meta wrote no meta label")),
		},
	}
}
