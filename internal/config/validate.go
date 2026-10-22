package config

import (
	"fmt"
	"slices"
	"strings"
)

// Validate reports the configuration mistakes that cannot be given a sensible
// meaning, rather than resolving them into a default and writing the wrong
// thing into somebody's schema files. It is called before a run, and by Load, so
// a typo fails at the point it is read.
func (c *Config) Validate() error {
	var problems []string
	problems = append(problems, c.validateTags()...)
	if len(problems) == 0 {
		return nil
	}
	return fmt.Errorf("%s", strings.Join(problems, "\n"))
}

func (c *Config) validateTags() []string {
	var problems []string
	if f := strOr(c.Tags.Folders, FolderTagsOff); !slices.Contains(
		[]string{FolderTagsOff, FolderTagsRules, FolderTagsAll}, f) {
		problems = append(problems, fmt.Sprintf(
			"tags.folders: %q is not one of %q, %q or %q", f,
			FolderTagsOff, FolderTagsRules, FolderTagsAll))
	}

	seen := map[string]bool{}
	for i, rule := range c.Tags.Rules {
		where := fmt.Sprintf("tags.rules[%d]", i)
		if rule.Key != "" {
			where = fmt.Sprintf("tags.rules[%d] (%s)", i, rule.Key)
		}

		switch {
		case rule.Key == "":
			problems = append(problems, where+": key is required, since a rule is about one label key")
		case seen[rule.Key]:
			problems = append(problems, where+": a second rule for the same key, and only one can decide it")
		default:
			seen[rule.Key] = true
		}

		// Without a vocabulary there is no ladder, so "strongest" would have nothing
		// to compare and every value would be unknown.
		if len(rule.Values) == 0 {
			problems = append(problems, where+": values is required, weakest first")
		}
		dupes := map[string]bool{}
		for _, v := range rule.Values {
			switch {
			case v == "":
				problems = append(problems, where+": values contains an empty value")
			case dupes[v]:
				problems = append(problems, fmt.Sprintf("%s: values lists %q twice, so its strength is ambiguous", where, v))
			default:
				dupes[v] = true
			}
		}

		if r := strOr(rule.Resolve, ResolveStrongest); !slices.Contains(
			[]string{ResolveStrongest, ResolveWeakest, ResolveFirst}, r) {
			problems = append(problems, fmt.Sprintf("%s: resolve: %q is not one of %q, %q or %q",
				where, r, ResolveStrongest, ResolveWeakest, ResolveFirst))
		}
		if i := strOr(rule.Inherit, RuleInheritCut); !slices.Contains(
			[]string{RuleInheritCut, RuleInheritAll}, i) {
			problems = append(problems, fmt.Sprintf("%s: inherit: %q is not %q or %q",
				where, i, RuleInheritCut, RuleInheritAll))
		}
		if w := strOr(rule.Write, TagRuleWriteTags); !slices.Contains(
			[]string{TagRuleWriteTags, TagRuleWriteMeta, TagRuleWriteBoth}, w) {
			problems = append(problems, fmt.Sprintf("%s: write: %q is not one of %q, %q or %q",
				where, w, TagRuleWriteTags, TagRuleWriteMeta, TagRuleWriteBoth))
		}
		if d := strOr(rule.Default, ""); d != "" && !slices.Contains(rule.Values, d) {
			problems = append(problems, fmt.Sprintf("%s: default: %q is not one of the values %v",
				where, d, rule.Values))
		}
	}
	return problems
}
