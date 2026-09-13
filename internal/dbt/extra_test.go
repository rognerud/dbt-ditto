package dbt

import (
	"encoding/json"
	"reflect"
	"testing"
)

const columnWithPolicyTags = `{
  "name": "email",
  "description": "Customer email.",
  "data_type": "STRING",
  "policy_tags": ["projects/bq/locations/eu/taxonomies/1/policyTags/2"],
  "quote": true
}`

// Nothing is captured by default: a map per column for fields nobody asked to
// propagate is not free.
func TestExtraColumnKeysDefaultsToCapturingNothing(t *testing.T) {
	var c Column
	if err := json.Unmarshal([]byte(columnWithPolicyTags), &c); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if c.Extra != nil {
		t.Fatalf("Extra = %v, want nil with no keys configured", c.Extra)
	}
	if c.Name != "email" || c.DataType != "STRING" {
		t.Fatalf("modelled fields lost: %+v", c)
	}
}

func TestExtraColumnKeysCapturesOnlyWhatItNames(t *testing.T) {
	defer withExtraKeys(t, "policy_tags")()

	var c Column
	if err := json.Unmarshal([]byte(columnWithPolicyTags), &c); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	want := map[string]any{
		"policy_tags": []any{"projects/bq/locations/eu/taxonomies/1/policyTags/2"},
	}
	if !reflect.DeepEqual(c.Extra, want) {
		t.Fatalf("Extra = %#v, want %#v", c.Extra, want)
	}
	if c.Description != "Customer email." {
		t.Fatalf("modelled fields lost: %+v", c)
	}
}

// A named key the column does not have must not become a present-but-empty
// entry: propagation has to tell "no value" from "an empty value".
func TestAbsentAndNullExtraKeysAreNotRecorded(t *testing.T) {
	defer withExtraKeys(t, "policy_tags", "missing")()

	var c Column
	if err := json.Unmarshal([]byte(`{"name":"id","policy_tags":null}`), &c); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if c.Extra != nil {
		t.Fatalf("Extra = %#v, want nil: one key is absent and the other is null", c.Extra)
	}
}

func withExtraKeys(t *testing.T, keys ...string) func() {
	t.Helper()
	prev := ExtraColumnKeys
	ExtraColumnKeys = keys
	return func() { ExtraColumnKeys = prev }
}
