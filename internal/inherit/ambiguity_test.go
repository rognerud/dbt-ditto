package inherit

import (
	"reflect"
	"testing"

	"github.com/rognerud/dbt-ditto/internal/config"
)

// The annotation names the ancestors that were overruled, not the one that won:
// the winner is already recorded by the progenitor key.
func TestAmbiguityMetaNamesTheDissenters(t *testing.T) {
	// `model.p.other` sorts before `seed.p.raw`, so the model claims the column
	// and the seed is the dissenter.
	won := node("p", "other", nil, col("id", "The identifier, as the model puts it."))
	lost := seed("p", "raw", col("id", "The identifier, as the seed puts it."))
	down := node("p", "stg", []string{"seed.p.raw", "model.p.other"}, col("id", ""))
	r := resolverFor(t, func(c *config.Config) {
		c.Inheritance.AmbiguityMeta = ptr(true)
	}, project("p", won, lost, down))

	got := columnDoc(t, r.Resolve(down, Existing{}), "id")

	meta := metaMap(got.Meta)
	want := []string{"seed.p.raw"}
	if !reflect.DeepEqual(meta[config.DefaultAmbiguityKey], want) {
		t.Errorf("%s = %#v, want %#v", config.DefaultAmbiguityKey, meta[config.DefaultAmbiguityKey], want)
	}
	if meta[config.DefaultProgenitorKey] != "model.p.other" {
		t.Errorf("progenitor = %#v, want the winner model.p.other", meta[config.DefaultProgenitorKey])
	}
	if got.Description != "The identifier, as the model puts it." {
		t.Errorf("description = %q, want the winner's", got.Description)
	}
}

// Off unless asked for: it writes meta dbt-osmosis would not, and parity with an
// existing dbt-osmosis project is the promise.
func TestAmbiguityMetaIsOffByDefault(t *testing.T) {
	won := seed("p", "raw", col("id", "One wording."))
	lost := node("p", "other", nil, col("id", "Another wording."))
	down := node("p", "stg", []string{"seed.p.raw", "model.p.other"}, col("id", ""))
	r := resolverFor(t, nil, project("p", won, lost, down))

	got := columnDoc(t, r.Resolve(down, Existing{}), "id")

	if _, ok := metaMap(got.Meta)[config.DefaultAmbiguityKey]; ok {
		t.Errorf("annotation written without being asked for: %v", metaOrder(got.Meta))
	}
}

// A column documented locally did not inherit anything, so there is no
// arbitrary choice to disclose — and an annotation left over from when it did
// inherit is a stale claim, so it is removed rather than carried forward.
func TestAmbiguityMetaClearedOnceSettledLocally(t *testing.T) {
	won := seed("p", "raw", col("id", "One wording."))
	lost := node("p", "other", nil, col("id", "Another wording."))
	down := node("p", "stg", []string{"seed.p.raw", "model.p.other"},
		col("id", "Settled by hand.",
			withMeta(config.DefaultAmbiguityKey, []string{"model.p.other"})))
	r := resolverFor(t, func(c *config.Config) {
		c.Inheritance.AmbiguityMeta = ptr(true)
	}, project("p", won, lost, down))

	got := columnDoc(t, r.Resolve(down, Existing{}), "id")

	if _, ok := metaMap(got.Meta)[config.DefaultAmbiguityKey]; ok {
		t.Errorf("stale annotation kept on a locally documented column: %v", metaOrder(got.Meta))
	}
}

// The annotation belongs to the column it was computed for. Inheriting an
// ancestor's copy would tell a downstream reader that *their* parents disagreed,
// which is a different claim and usually a false one.
func TestAmbiguityMetaIsNotInheritedFromAnAncestor(t *testing.T) {
	up := seed("p", "raw", col("id", "The identifier.",
		withMeta("owner", "team", config.DefaultAmbiguityKey, []string{"model.p.somewhere"})))
	down := node("p", "stg", []string{"seed.p.raw"}, col("id", ""))
	r := resolverFor(t, func(c *config.Config) {
		c.Inheritance.AmbiguityMeta = ptr(true)
	}, project("p", up, down))

	got := columnDoc(t, r.Resolve(down, Existing{}), "id")

	meta := metaMap(got.Meta)
	if _, ok := meta[config.DefaultAmbiguityKey]; ok {
		t.Errorf("ancestor's annotation inherited: %v", metaOrder(got.Meta))
	}
	if meta["owner"] != "team" {
		t.Errorf("ordinary meta stopped being inherited: %v", meta)
	}
}

func ptr[T any](v T) *T { return &v }
