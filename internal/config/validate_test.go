package config_test

import (
	"strings"
	"testing"

	"github.com/rognerud/dbt-ditto/internal/config"
)

func ptr[T any](v T) *T { return &v }

func sensitivity() config.TagRule {
	return config.TagRule{
		Key:    "data-sensitivity",
		Values: []string{"public", "internal", "confidential", "sensitive"},
	}
}

// A rule that cannot be given a meaning stops the run rather than being
// resolved into a default: what it would otherwise write is a claim about
// somebody's data.
func TestValidateRejectsRulesWithNoMeaning(t *testing.T) {
	cases := []struct {
		name string
		tags config.Tags
		want string
	}{
		{
			name: "unknown folder mode",
			tags: config.Tags{Folders: ptr("sometimes")},
			want: `tags.folders: "sometimes"`,
		},
		{
			name: "no key",
			tags: config.Tags{Rules: []config.TagRule{{Values: []string{"public"}}}},
			want: "key is required",
		},
		{
			name: "no values",
			tags: config.Tags{Rules: []config.TagRule{{Key: "data-sensitivity"}}},
			want: "values is required",
		},
		{
			name: "two rules for one key",
			tags: config.Tags{Rules: []config.TagRule{sensitivity(), sensitivity()}},
			want: "a second rule for the same key",
		},
		{
			name: "a value listed twice",
			tags: config.Tags{Rules: []config.TagRule{{
				Key: "data-sensitivity", Values: []string{"public", "public"},
			}}},
			want: `lists "public" twice`,
		},
		{
			name: "an empty value",
			tags: config.Tags{Rules: []config.TagRule{{
				Key: "data-sensitivity", Values: []string{"public", ""},
			}}},
			want: "values contains an empty value",
		},
		{
			name: "unknown resolve",
			tags: config.Tags{Rules: []config.TagRule{{
				Key: "data-sensitivity", Values: []string{"public"}, Resolve: ptr("loudest"),
			}}},
			want: `resolve: "loudest"`,
		},
		{
			name: "unknown inherit",
			tags: config.Tags{Rules: []config.TagRule{{
				Key: "data-sensitivity", Values: []string{"public"}, Inherit: ptr("sometimes"),
			}}},
			want: `inherit: "sometimes"`,
		},
		{
			name: "unknown write",
			tags: config.Tags{Rules: []config.TagRule{{
				Key: "data-sensitivity", Values: []string{"public"}, Write: ptr("stdout"),
			}}},
			want: `write: "stdout"`,
		},
		{
			name: "a default outside the vocabulary",
			tags: config.Tags{Rules: []config.TagRule{{
				Key: "data-sensitivity", Values: []string{"public"}, Default: ptr("secret"),
			}}},
			want: `default: "secret" is not one of the values`,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg := &config.Config{
				Projects: []config.ProjectRef{{Path: "."}},
				Tags:     c.tags,
			}
			err := cfg.Validate()
			if err == nil {
				t.Fatalf("accepted a configuration that cannot be acted on")
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("error does not say what is wrong:\ngot:  %s\nwant it to mention: %s",
					err, c.want)
			}
			// The message names the rule, since a project may configure several.
			if len(c.tags.Rules) > 0 && !strings.Contains(err.Error(), "tags.rules[") {
				t.Errorf("error does not say which rule: %s", err)
			}
		})
	}
}

func TestValidateAcceptsAWorkableRule(t *testing.T) {
	rule := sensitivity()
	rule.Resolve = ptr(config.ResolveStrongest)
	rule.Inherit = ptr(config.RuleInheritAll)
	rule.Write = ptr(config.TagRuleWriteBoth)
	rule.Default = ptr("public")
	cfg := &config.Config{
		Projects: []config.ProjectRef{{Path: "."}},
		Tags:     config.Tags{Folders: ptr(config.FolderTagsRules), Rules: []config.TagRule{rule}},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("rejected a workable rule: %s", err)
	}
}

// A project that configures nothing is valid, which is what keeps this a
// feature rather than a new requirement.
func TestValidateAcceptsNoTagConfigurationAtAll(t *testing.T) {
	cfg := &config.Config{Projects: []config.ProjectRef{{Path: "."}}}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("an unconfigured project was rejected: %s", err)
	}
}

// Every problem is reported at once: fixing a config file one error per run is
// a worse experience than reading the list.
func TestValidateReportsEveryProblem(t *testing.T) {
	cfg := &config.Config{
		Projects: []config.ProjectRef{{Path: "."}},
		Tags: config.Tags{
			Folders: ptr("sometimes"),
			Rules:   []config.TagRule{{Key: "", Values: nil}},
		},
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("accepted a configuration with three problems")
	}
	for _, want := range []string{"tags.folders", "key is required", "values is required"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("%q was not reported:\n%s", want, err)
		}
	}
}

// Resolve indexes the vocabulary, and the strength comparison is what every
// rule is decided by.
func TestResolvedRuleRanksItsVocabulary(t *testing.T) {
	cfg := &config.Config{Tags: config.Tags{Rules: []config.TagRule{sensitivity()}}}
	r := cfg.Resolve()

	rule, ok := r.TagRuleFor("data-sensitivity")
	if !ok {
		t.Fatal("the rule did not survive Resolve")
	}
	if rule.Resolve != config.ResolveStrongest || rule.Write != config.TagRuleWriteTags {
		t.Errorf("defaults are %q/%q, want %q/%q",
			rule.Resolve, rule.Write, config.ResolveStrongest, config.TagRuleWriteTags)
	}
	// Cut by default: a value written halfway down the DAG is a decision, and
	// everything below it should inherit that rather than re-derive the source.
	if !rule.Cuts() {
		t.Errorf("inherit defaults to %q, want %q", rule.Inherit, config.RuleInheritCut)
	}
	public, _ := rule.Rank("public")
	sensitive, _ := rule.Rank("sensitive")
	if !(public < sensitive) {
		t.Errorf("public ranks %d and sensitive %d: the ladder is upside down", public, sensitive)
	}
	if _, known := rule.Rank("secret"); known {
		t.Error("a value outside the vocabulary was ranked")
	}
	if r.FolderTagsRead() {
		t.Error("folders are read without being asked for")
	}
}
