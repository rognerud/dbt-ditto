package inherit

import (
	"reflect"
	"slices"
	"testing"

	"github.com/rognerud/dbt-ditto/internal/config"
	"github.com/rognerud/dbt-ditto/internal/dbt"
)

// sensitivity is the rule every case here is about: four values, weakest first,
// which is the shape the feature exists for.
func sensitivity(tweak ...func(*config.TagRule)) config.TagRule {
	r := config.TagRule{
		Key:    "data-sensitivity",
		Values: []string{"public", "internal", "confidential", "sensitive"},
	}
	for _, f := range tweak {
		f(&r)
	}
	return r
}

// withRules is the config tweak resolverFor takes.
func withRules(rules ...config.TagRule) func(*config.Config) {
	return func(c *config.Config) { c.Tags.Rules = rules }
}

// withFolders sets tags.folders alongside the rules.
func withFolders(mode string, rules ...config.TagRule) func(*config.Config) {
	return func(c *config.Config) {
		c.Tags.Folders = ptr(mode)
		c.Tags.Rules = rules
	}
}

// folderTags gives a project a `+tags:` rule at a folder prefix of the `models:`
// tree, as dbt_project.yml would.
func folderTags(p *dbt.Project, prefix []string, tags ...string) {
	folderTagsOf(p, "model", prefix, tags...)
}

func folderTagsOf(p *dbt.Project, kind string, prefix []string, tags ...string) {
	p.FolderTagRules = append(p.FolderTagRules,
		dbt.FolderTagRule{Kind: kind, Prefix: prefix, Tags: tags})
	dbt.SortFolderTagRules(p.FolderTagRules)
}

// inFolder puts a node at an fqn, which is what folder rules match against.
func inFolder(n *dbt.Node, segments ...string) *dbt.Node {
	n.FQN = append([]string{n.PackageName}, segments...)
	n.FQN = append(n.FQN, n.Name)
	return n
}

func hasTag(tags []string, want string) bool { return slices.Contains(tags, want) }

func assertTags(t *testing.T, got []string, want ...string) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("tags = %v, want %v", got, want)
	}
}

// --- resolving a ladder ----------------------------------------------------

// The headline promise: several ancestors, one of them stricter, and the column
// ends up at the strictest of them rather than carrying all four.
func TestRuleTakesTheStrongestUpstreamValue(t *testing.T) {
	a := seed("p", "raw_a", col("id", "", withTags("data-sensitivity:public")))
	b := seed("p", "raw_b", col("id", "", withTags("data-sensitivity:confidential")))
	down := node("p", "stg", []string{"seed.p.raw_a", "seed.p.raw_b"}, col("id", ""))
	r := resolverFor(t, withRules(sensitivity()), project("p", a, b, down))

	got := columnDoc(t, r.Resolve(down, Existing{}), "id")
	assertTags(t, got.Tags, "data-sensitivity:confidential")
	if m := metaMap(got.Meta); !reflect.DeepEqual(m[LabelDefaultsKey], []string{"data-sensitivity"}) {
		t.Errorf("%s = %v, want the key listed: a resolved value is a default a later run may revise",
			LabelDefaultsKey, m[LabelDefaultsKey])
	}
}

func TestRuleResolveWeakestTakesTheLeastStrictValue(t *testing.T) {
	a := seed("p", "raw_a", col("id", "", withTags("data-sensitivity:public")))
	b := seed("p", "raw_b", col("id", "", withTags("data-sensitivity:confidential")))
	down := node("p", "stg", []string{"seed.p.raw_a", "seed.p.raw_b"}, col("id", ""))
	rule := sensitivity(func(r *config.TagRule) { r.Resolve = ptr(config.ResolveWeakest) })
	r := resolverFor(t, withRules(rule), project("p", a, b, down))

	assertTags(t, columnDoc(t, r.Resolve(down, Existing{}), "id").Tags, "data-sensitivity:public")
}

// "first" is the precedence a description gets: the nearest branch decides,
// however strict a further one is. The two branches reach different depths and
// neither is altered on the way, so nothing is cut and both are candidates.
func TestRuleResolveFirstTakesTheNearestBranch(t *testing.T) {
	far := seed("p", "raw_far", col("id", "", withTags("data-sensitivity:sensitive")))
	through := node("p", "through", []string{"seed.p.raw_far"}, col("id", ""))
	near := seed("p", "raw_near", col("id", "", withTags("data-sensitivity:internal")))
	down := node("p", "stg", []string{"model.p.through", "seed.p.raw_near"}, col("id", ""))
	p := project("p", far, through, near, down)

	first := sensitivity(func(r *config.TagRule) { r.Resolve = ptr(config.ResolveFirst) })
	assertTags(t, columnDoc(t, resolverFor(t, withRules(first), p).Resolve(down, Existing{}), "id").Tags,
		"data-sensitivity:internal")

	// The same two branches, settled the other way, so the case cannot pass by
	// the depths happening not to matter.
	assertTags(t, columnDoc(t, resolverFor(t, withRules(sensitivity()), p).Resolve(down, Existing{}), "id").Tags,
		"data-sensitivity:sensitive")
}

func TestRuleDefaultAppliesWhenNothingUpstreamSaysAnything(t *testing.T) {
	up := seed("p", "raw", col("id", "The identifier."))
	down := node("p", "stg", []string{"seed.p.raw"}, col("id", ""))
	rule := sensitivity(func(r *config.TagRule) { r.Default = ptr("internal") })
	r := resolverFor(t, withRules(rule), project("p", up, down))

	got := columnDoc(t, r.Resolve(down, Existing{}), "id")
	assertTags(t, got.Tags, "data-sensitivity:internal")
	if m := metaMap(got.Meta); !reflect.DeepEqual(m[LabelDefaultsKey], []string{"data-sensitivity"}) {
		t.Errorf("the configured default was not recorded as a default: %v", m[LabelDefaultsKey])
	}
}

// --- explicit beats inherited ---------------------------------------------

func TestExplicitValueOnTheColumnOutranksAStricterAncestor(t *testing.T) {
	up := seed("p", "raw", col("id", "", withTags("data-sensitivity:sensitive")))
	down := node("p", "stg", []string{"seed.p.raw"},
		col("id", "", withTags("data-sensitivity:internal")))
	r := resolverFor(t, withRules(sensitivity()), project("p", up, down))

	got := columnDoc(t, r.Resolve(down, Existing{}), "id")
	assertTags(t, got.Tags, "data-sensitivity:internal")
	if hasTag(got.Tags, "data-sensitivity:sensitive") {
		t.Error("the inherited value was kept alongside the explicit one, so the column has two")
	}
	if _, listed := metaMap(got.Meta)[LabelDefaultsKey]; listed {
		t.Error("an explicit value was recorded as a default, and a later run would overwrite it")
	}
}

// The other half of the same promise: a value this tool wrote last run is not
// explicit, however identical it looks, so the ladder can still move.
func TestAValuePreviouslyWrittenAsADefaultIsRevised(t *testing.T) {
	up := seed("p", "raw", col("id", "", withTags("data-sensitivity:sensitive")))
	down := node("p", "stg", []string{"seed.p.raw"},
		col("id", "", withTags("data-sensitivity:internal"),
			withMeta(LabelDefaultsKey, []any{"data-sensitivity"})))
	r := resolverFor(t, withRules(sensitivity()), project("p", up, down))

	assertTags(t, columnDoc(t, r.Resolve(down, Existing{}), "id").Tags, "data-sensitivity:sensitive")
}

// A default can go down as well as up: the point of recording it is that it is
// this tool's, not a decision.
func TestADefaultIsDowngradedWhenUpstreamRelaxes(t *testing.T) {
	up := seed("p", "raw", col("id", "", withTags("data-sensitivity:public")))
	down := node("p", "stg", []string{"seed.p.raw"},
		col("id", "", withTags("data-sensitivity:sensitive"),
			withMeta(LabelDefaultsKey, []any{"data-sensitivity"})))
	r := resolverFor(t, withRules(sensitivity()), project("p", up, down))

	assertTags(t, columnDoc(t, r.Resolve(down, Existing{}), "id").Tags, "data-sensitivity:public")
}

func TestADefaultIsRemovedWhenUpstreamStopsSayingAnything(t *testing.T) {
	up := seed("p", "raw", col("id", "The identifier."))
	down := node("p", "stg", []string{"seed.p.raw"},
		col("id", "", withTags("core", "data-sensitivity:internal"),
			withMeta(LabelDefaultsKey, []any{"data-sensitivity"})))
	r := resolverFor(t, withRules(sensitivity()), project("p", up, down))

	got := columnDoc(t, r.Resolve(down, Existing{}), "id")
	assertTags(t, got.Tags, "core")
	if _, listed := metaMap(got.Meta)[LabelDefaultsKey]; listed {
		t.Error("the marker outlived the value it was about")
	}
}

// --- folders --------------------------------------------------------------

func TestFolderTagIsCanonicalOverAStricterAncestor(t *testing.T) {
	up := seed("p", "raw", col("id", "", withTags("data-sensitivity:sensitive")))
	down := inFolder(node("p", "stg", []string{"seed.p.raw"}, col("id", "")), "staging")
	p := project("p", up, down)
	folderTags(p, []string{"staging"}, "data-sensitivity:internal")
	r := resolverFor(t, withFolders(config.FolderTagsRules, sensitivity()), p)

	got := columnDoc(t, r.Resolve(down, Existing{}), "id")
	assertTags(t, got.Tags, "data-sensitivity:internal")
	if _, listed := metaMap(got.Meta)[LabelDefaultsKey]; listed {
		t.Error("a folder's declaration was recorded as a default rather than as a decision")
	}
}

// In "rules" mode a folder decides the keys it has a rule for and stamps
// nothing else onto the columns.
func TestFolderTagsRulesLeavesUnrelatedFolderTagsAlone(t *testing.T) {
	up := seed("p", "raw", col("id", "The identifier."))
	down := inFolder(node("p", "stg", []string{"seed.p.raw"}, col("id", "")), "staging")
	p := project("p", up, down)
	folderTags(p, []string{"staging"}, "nightly", "data-sensitivity:confidential")
	r := resolverFor(t, withFolders(config.FolderTagsRules, sensitivity()), p)

	got := columnDoc(t, r.Resolve(down, Existing{}), "id")
	assertTags(t, got.Tags, "data-sensitivity:confidential")
}

func TestFolderTagsAllWritesEveryFolderTagOntoTheColumn(t *testing.T) {
	up := seed("p", "raw", col("id", "The identifier."))
	down := inFolder(node("p", "stg", []string{"seed.p.raw"}, col("id", "")), "staging")
	p := project("p", up, down)
	folderTags(p, []string{"staging"}, "nightly")
	r := resolverFor(t, withFolders(config.FolderTagsAll), p)

	assertTags(t, columnDoc(t, r.Resolve(down, Existing{}), "id").Tags, "nightly")
}

func TestFolderTagsOffIgnoresTheFolderEntirely(t *testing.T) {
	up := seed("p", "raw", col("id", "The identifier."))
	down := inFolder(node("p", "stg", []string{"seed.p.raw"}, col("id", "")), "staging")
	p := project("p", up, down)
	folderTags(p, []string{"staging"}, "nightly", "data-sensitivity:confidential")
	r := resolverFor(t, withRules(sensitivity()), p)

	assertTags(t, columnDoc(t, r.Resolve(down, Existing{}), "id").Tags)
}

// A folder declaration describes the data in it, so it travels down the graph
// to everything built from it.
func TestAnAncestorsFolderTagReachesDownstream(t *testing.T) {
	up := inFolder(seed("p", "raw", col("id", "The identifier.")), "raw")
	down := inFolder(node("p", "stg", []string{"seed.p.raw"}, col("id", "")), "staging")
	p := project("p", up, down)
	folderTagsOf(p, "seed", []string{"raw"}, "data-sensitivity:confidential")
	r := resolverFor(t, withFolders(config.FolderTagsRules, sensitivity()), p)

	got := columnDoc(t, r.Resolve(down, Existing{}), "id")
	assertTags(t, got.Tags, "data-sensitivity:confidential")
	if m := metaMap(got.Meta); !reflect.DeepEqual(m[LabelDefaultsKey], []string{"data-sensitivity"}) {
		t.Errorf("an inherited folder value is a default, not a decision about this column: %v",
			m[LabelDefaultsKey])
	}
}

// dbt accumulates `+tags:` down the tree rather than letting the nearest win.
func TestFolderTagsAccumulateFromEveryFolderAbove(t *testing.T) {
	down := inFolder(node("p", "stg", nil, col("id", "The identifier.")), "marts", "finance")
	p := project("p", down)
	folderTags(p, []string{"marts"}, "marts")
	folderTags(p, []string{"marts", "finance"}, "finance")
	r := resolverFor(t, withFolders(config.FolderTagsAll), p)

	assertTags(t, columnDoc(t, r.Resolve(down, Existing{}), "id").Tags, "marts", "finance")
}

// --- routing --------------------------------------------------------------

func TestRuleWriteMetaPutsTheValueUnderTheLabelKey(t *testing.T) {
	up := seed("p", "raw", col("id", "", withTags("data-sensitivity:confidential")))
	down := node("p", "stg", []string{"seed.p.raw"}, col("id", ""))
	rule := sensitivity(func(r *config.TagRule) { r.Write = ptr(config.TagRuleWriteMeta) })
	r := resolverFor(t, withRules(rule), project("p", up, down))

	got := columnDoc(t, r.Resolve(down, Existing{}), "id")
	if hasTag(got.Tags, "data-sensitivity:confidential") {
		t.Errorf("write: meta still wrote a tag: %v", got.Tags)
	}
	labels, ok := metaMap(got.Meta)[config.DefaultLabelMetaKey].(*dbt.OrderedMap)
	if !ok {
		t.Fatalf("no %q map in meta: %v", config.DefaultLabelMetaKey, metaMap(got.Meta))
	}
	if v, _ := labels.Get("data-sensitivity"); v != "confidential" {
		t.Errorf("meta labels data-sensitivity = %v, want confidential", v)
	}
}

func TestRuleWriteBothWritesTagAndMeta(t *testing.T) {
	up := seed("p", "raw", col("id", "", withTags("data-sensitivity:confidential")))
	down := node("p", "stg", []string{"seed.p.raw"}, col("id", ""))
	rule := sensitivity(func(r *config.TagRule) { r.Write = ptr(config.TagRuleWriteBoth) })
	r := resolverFor(t, withRules(rule), project("p", up, down))

	got := columnDoc(t, r.Resolve(down, Existing{}), "id")
	if !hasTag(got.Tags, "data-sensitivity:confidential") {
		t.Errorf("write: both wrote no tag: %v", got.Tags)
	}
	if _, ok := metaMap(got.Meta)[config.DefaultLabelMetaKey]; !ok {
		t.Errorf("write: both wrote no meta: %v", metaMap(got.Meta))
	}
}

// A value in the nested label map is read as readily as a tag, so a project
// routing provider labels into meta needs no second spelling.
func TestRuleReadsTheValueOutOfMeta(t *testing.T) {
	labels := dbt.NewOrderedMap()
	labels.Set("data-sensitivity", "sensitive")
	up := seed("p", "raw", col("id", "", withMeta(config.DefaultLabelMetaKey, labels)))
	down := node("p", "stg", []string{"seed.p.raw"}, col("id", ""))
	r := resolverFor(t, withRules(sensitivity()), project("p", up, down))

	assertTags(t, columnDoc(t, r.Resolve(down, Existing{}), "id").Tags, "data-sensitivity:sensitive")
}

// Writing through the knowledge map must not reach the ancestor it was cloned
// from, which every other column of that ancestor is still about to be read for.
func TestWritingALabelDoesNotEditTheAncestor(t *testing.T) {
	labels := dbt.NewOrderedMap()
	labels.Set("data-sensitivity", "public")
	upCol := col("id", "The identifier.", withMeta(config.DefaultLabelMetaKey, labels))
	up := seed("p", "raw", upCol)
	strict := seed("p", "raw_b", col("id", "", withTags("data-sensitivity:sensitive")))
	down := node("p", "stg", []string{"seed.p.raw", "seed.p.raw_b"}, col("id", ""))
	rule := sensitivity(func(r *config.TagRule) { r.Write = ptr(config.TagRuleWriteMeta) })
	r := resolverFor(t, withRules(rule), project("p", up, strict, down))

	r.Resolve(down, Existing{})
	if v, _ := labels.Get("data-sensitivity"); v != "public" {
		t.Errorf("the ancestor's own label map was rewritten to %v", v)
	}
}

// --- values outside the vocabulary ----------------------------------------

func TestAValueOutsideTheVocabularyIsReportedAndLeftAlone(t *testing.T) {
	up := seed("p", "raw", col("id", "", withTags("data-sensitivity:secrit")))
	down := node("p", "stg", []string{"seed.p.raw"}, col("id", ""))
	rule := sensitivity(func(r *config.TagRule) { r.Default = ptr("public") })
	r := resolverFor(t, withRules(rule), project("p", up, down))

	doc := r.Resolve(down, Existing{})
	got := columnDoc(t, doc, "id")
	if !hasTag(got.Tags, "data-sensitivity:secrit") {
		t.Errorf("the misspelled value was dropped rather than reported: %v", got.Tags)
	}
	if !hasTag(got.Tags, "data-sensitivity:public") {
		t.Errorf("the default did not apply although nothing comparable was found: %v", got.Tags)
	}
	if !warned(doc, WarnLabelUnknown) {
		t.Errorf("nothing was said about a value outside the vocabulary: %v", doc.Warnings)
	}
}

func warned(doc *NodeDoc, kind string) bool {
	for _, w := range doc.Warnings {
		if w.Kind == kind {
			return true
		}
	}
	return false
}

// --- the feature switched off ---------------------------------------------

// With no rules and no folders the tag union is exactly what it was, which is
// what keeps this a feature rather than a change of behaviour.
func TestWithoutRulesTagsAreStillTheUnion(t *testing.T) {
	far := seed("p", "raw", col("id", "", withTags("data-sensitivity:public")))
	mid := node("p", "mid", []string{"seed.p.raw"}, col("id", "", withTags("data-sensitivity:confidential")))
	down := node("p", "stg", []string{"model.p.mid"}, col("id", ""))
	r := resolverFor(t, nil, project("p", far, mid, down))

	got := columnDoc(t, r.Resolve(down, Existing{}), "id")
	if len(got.Tags) != 2 {
		t.Errorf("tags = %v, want both values: nothing was configured to collapse them", got.Tags)
	}
	if _, listed := metaMap(got.Meta)[LabelDefaultsKey]; listed {
		t.Error("a marker was written although no rule ever ran")
	}
}

// --- the tag format ------------------------------------------------------

func TestTagTemplateRoundTrips(t *testing.T) {
	cases := []struct {
		format, key, value, tag string
		parsable                bool
	}{
		{format: "{key}:{value}", key: "data-sensitivity", value: "public",
			tag: "data-sensitivity:public", parsable: true},
		{format: "{key}={value}", key: "a", value: "b", tag: "a=b", parsable: true},
		{format: "dbt_{key}__{value}", key: "a", value: "b", tag: "dbt_a__b", parsable: true},
		// No separator: `ab` could be read as any split of the two fields.
		{format: "{key}{value}", parsable: false},
		// Neither field: nothing to read a pair out of.
		{format: "static", parsable: false},
	}
	for _, c := range cases {
		tmpl := newTagTemplate(c.format)
		if tmpl.parsable != c.parsable {
			t.Errorf("%q parsable = %v, want %v", c.format, tmpl.parsable, c.parsable)
			continue
		}
		if !c.parsable {
			continue
		}
		if got := tmpl.render(c.key, c.value); got != c.tag {
			t.Errorf("%q render = %q, want %q", c.format, got, c.tag)
		}
		key, value, ok := tmpl.parse(c.tag)
		if !ok || key != c.key || value != c.value {
			t.Errorf("%q parse(%q) = %q/%q/%v, want %q/%q/true",
				c.format, c.tag, key, value, ok, c.key, c.value)
		}
	}
}

// A tag that is not a pair at all must not be read as one, or an ordinary tag
// would start taking part in a rule.
func TestTagTemplateIgnoresAPlainTag(t *testing.T) {
	tmpl := newTagTemplate(config.DefaultTagFormat)
	if _, _, ok := tmpl.parse("core"); ok {
		t.Error("a plain tag parsed as a key:value pair")
	}
	if _, _, ok := tmpl.parse(":public"); ok {
		t.Error("a pair with no key parsed")
	}
}

// A `models:` declaration must not reach a seed that happens to sit at the same
// depth: the section names the resource type it configures.
func TestFolderTagsDoNotCrossSections(t *testing.T) {
	up := inFolder(seed("p", "raw", col("id", "The identifier.")), "staging")
	down := inFolder(node("p", "stg", []string{"seed.p.raw"}, col("id", "")), "staging")
	p := project("p", up, down)
	folderTagsOf(p, "model", []string{"staging"}, "built")
	r := resolverFor(t, withFolders(config.FolderTagsAll), p)

	assertTags(t, columnDoc(t, r.Resolve(down, Existing{}), "id").Tags, "built")
	if got := p.FolderTags(up); got != nil {
		t.Errorf("a models: declaration tagged a seed: %v", got)
	}
}

// --- alterations halfway down the DAG -------------------------------------

// The reason `inherit: cut` is the default: a column is masked, hashed or
// bucketed in a staging model and somebody writes the weaker value there. Every
// model below it has to agree, or the alteration is only cosmetic.
func TestAnAlterationHalfwayDownIsWhatEverythingBelowInherits(t *testing.T) {
	raw := seed("p", "raw", col("email", "", withTags("data-sensitivity:sensitive")))
	hashed := node("p", "stg_hashed", []string{"seed.p.raw"},
		col("email", "", withTags("data-sensitivity:internal")))
	mart := node("p", "dim", []string{"model.p.stg_hashed"}, col("email", ""))
	r := resolverFor(t, withRules(sensitivity()), project("p", raw, hashed, mart))

	got := columnDoc(t, r.Resolve(mart, Existing{}), "email")
	assertTags(t, got.Tags, "data-sensitivity:internal")
	// The mart did not decide anything, so what it carries is still a default.
	if m := metaMap(got.Meta); !reflect.DeepEqual(m[LabelDefaultsKey], []string{"data-sensitivity"}) {
		t.Errorf("%s = %v, want the key listed", LabelDefaultsKey, m[LabelDefaultsKey])
	}
}

// The cut is one branch, not the whole generation: a second, unaltered branch
// still carries its source's classification the whole way down.
func TestACutOnOneBranchDoesNotCutAnother(t *testing.T) {
	raw := seed("p", "raw", col("email", "", withTags("data-sensitivity:sensitive")))
	hashed := node("p", "stg_hashed", []string{"seed.p.raw"},
		col("email", "", withTags("data-sensitivity:public")))
	other := seed("p", "raw_other", col("email", "", withTags("data-sensitivity:confidential")))
	mart := node("p", "dim", []string{"model.p.stg_hashed", "seed.p.raw_other"}, col("email", ""))
	r := resolverFor(t, withRules(sensitivity()), project("p", raw, hashed, other, mart))

	assertTags(t, columnDoc(t, r.Resolve(mart, Existing{}), "email").Tags,
		"data-sensitivity:confidential")
}

// A value an earlier run wrote is not an alteration, so it cuts nothing: the
// chain is re-derived from whatever decided it in the first place.
func TestADefaultHalfwayDownDoesNotCutTheChain(t *testing.T) {
	raw := seed("p", "raw", col("email", "", withTags("data-sensitivity:sensitive")))
	// `stg` carries the weaker value *and* the marker saying this tool put it
	// there — the state an interrupted or stale run leaves behind.
	stg := node("p", "stg", []string{"seed.p.raw"},
		col("email", "", withTags("data-sensitivity:internal"),
			withMeta(LabelDefaultsKey, []any{"data-sensitivity"})))
	mart := node("p", "dim", []string{"model.p.stg"}, col("email", ""))
	r := resolverFor(t, withRules(sensitivity()), project("p", raw, stg, mart))

	assertTags(t, columnDoc(t, r.Resolve(mart, Existing{}), "email").Tags,
		"data-sensitivity:sensitive")
}

// A folder is a declaration too, so it cuts for everything below it.
func TestAFolderCutsTheBranchBelowIt(t *testing.T) {
	raw := seed("p", "raw", col("email", "", withTags("data-sensitivity:sensitive")))
	stg := inFolder(node("p", "stg", []string{"seed.p.raw"}, col("email", "")), "masked")
	mart := node("p", "dim", []string{"model.p.stg"}, col("email", ""))
	p := project("p", raw, stg, mart)
	folderTags(p, []string{"masked"}, "data-sensitivity:internal")
	r := resolverFor(t, withFolders(config.FolderTagsRules, sensitivity()), p)

	assertTags(t, columnDoc(t, r.Resolve(mart, Existing{}), "email").Tags,
		"data-sensitivity:internal")
}

// `inherit: all` is the other reading, for whoever wants a classification to be
// a floor nothing downstream can write its way out of.
func TestInheritAllRefusesToBeWeakenedHalfwayDown(t *testing.T) {
	raw := seed("p", "raw", col("email", "", withTags("data-sensitivity:sensitive")))
	hashed := node("p", "stg_hashed", []string{"seed.p.raw"},
		col("email", "", withTags("data-sensitivity:internal")))
	mart := node("p", "dim", []string{"model.p.stg_hashed"}, col("email", ""))
	rule := sensitivity(func(r *config.TagRule) { r.Inherit = ptr(config.RuleInheritAll) })
	r := resolverFor(t, withRules(rule), project("p", raw, hashed, mart))

	// The altered model keeps its own value — it is explicit, and that is local.
	assertTags(t, columnDoc(t, r.Resolve(hashed, Existing{}), "email").Tags,
		"data-sensitivity:internal")
	// Below it, the source is read again and wins.
	assertTags(t, columnDoc(t, r.Resolve(mart, Existing{}), "email").Tags,
		"data-sensitivity:sensitive")
}

// Two branches rejoining must not make the walk exponential, nor the answer
// depend on which branch was followed first.
func TestCutWalkHandlesADiamond(t *testing.T) {
	raw := seed("p", "raw", col("email", "", withTags("data-sensitivity:confidential")))
	left := node("p", "left", []string{"seed.p.raw"}, col("email", ""))
	right := node("p", "right", []string{"seed.p.raw"}, col("email", ""))
	mart := node("p", "dim", []string{"model.p.left", "model.p.right"}, col("email", ""))
	r := resolverFor(t, withRules(sensitivity()), project("p", raw, left, right, mart))

	assertTags(t, columnDoc(t, r.Resolve(mart, Existing{}), "email").Tags,
		"data-sensitivity:confidential")
}

// A manifest written by hand can name a cycle; the walk has to end anyway.
func TestCutWalkTerminatesOnACycle(t *testing.T) {
	a := node("p", "a", []string{"model.p.b"}, col("email", ""))
	b := node("p", "b", []string{"model.p.a"},
		col("email", "", withTags("data-sensitivity:confidential")))
	r := resolverFor(t, withRules(sensitivity()), project("p", a, b))

	assertTags(t, columnDoc(t, r.Resolve(a, Existing{}), "email").Tags,
		"data-sensitivity:confidential")
}
