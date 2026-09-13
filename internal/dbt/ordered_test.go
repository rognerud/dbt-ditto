package dbt

import (
	"encoding/json"
	"math"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// Re-encoding has to reproduce key order, or a manifest round-tripped through
// dbt-ditto would reorder meta the analyst wrote.
func TestOrderedMapMarshalJSONKeepsKeyOrder(t *testing.T) {
	const in = `{"zulu":1,"alpha":"a","mike":true,"nested":{"x":1},"list":[1,2]}`
	var m OrderedMap
	if err := json.Unmarshal([]byte(in), &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	out, err := json.Marshal(&m)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	// The nested object is a plain map, so only the top level is order-stable.
	if got := string(out); !strings.HasPrefix(got, `{"zulu":1,"alpha":"a","mike":true,`) {
		t.Errorf("marshal = %s, want the top-level keys in their original order", got)
	}
}

// A nil map is a meta block that was never written, and has to encode as null
// rather than panicking or emitting `{}`.
func TestOrderedMapMarshalJSONOnNil(t *testing.T) {
	var m *OrderedMap
	out, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(out) != "null" {
		t.Errorf("marshal = %s, want null", out)
	}
}

func TestOrderedMapMarshalJSONOnEmpty(t *testing.T) {
	out, err := json.Marshal(NewOrderedMap())
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(out) != "{}" {
		t.Errorf("marshal = %s, want {}", out)
	}
}

// A value json cannot encode has to surface as an error rather than as a
// truncated object.
func TestOrderedMapMarshalJSONReportsABadValue(t *testing.T) {
	m := NewOrderedMap()
	m.Set("bad", func() {})
	if _, err := json.Marshal(m); err == nil {
		t.Error("marshalling a func value succeeded, want an error")
	}
}

func TestOrderedMapMarshalYAMLKeepsKeyOrder(t *testing.T) {
	m := NewOrderedMap()
	m.Set("zulu", 1)
	m.Set("alpha", "a")
	m.Set("mike", true)

	out, err := yaml.Marshal(m)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	want := "zulu: 1\nalpha: a\nmike: true\n"
	if string(out) != want {
		t.Errorf("marshal =\n%s\nwant\n%s", out, want)
	}
}

// A nil map is a meta block that was never written. yaml.Marshal short-circuits
// a nil pointer to `null` without calling the method, so the method is called
// directly: it is reached that way when an OrderedMap is a field of a node being
// encoded.
func TestOrderedMapMarshalYAMLOnNil(t *testing.T) {
	var m *OrderedMap
	v, err := m.MarshalYAML()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	n, ok := v.(*yaml.Node)
	if !ok {
		t.Fatalf("got %T, want a *yaml.Node", v)
	}
	if n.Kind != yaml.MappingNode || len(n.Content) != 0 {
		t.Errorf("node = %+v, want an empty mapping", n)
	}
}

// yaml.v3 panics rather than returning an error on a value it cannot encode, so
// MarshalYAML's error branch is unreachable from Marshal and is not asserted
// here. Every value reaching it came out of JSON, so it is encodable by
// construction.

func TestOrderedMapDeleteRemovesTheKeyAndItsPosition(t *testing.T) {
	m := NewOrderedMap()
	m.Set("a", 1)
	m.Set("b", 2)
	m.Set("c", 3)

	m.Delete("b")
	if want := []string{"a", "c"}; !reflect.DeepEqual(m.Keys(), want) {
		t.Errorf("keys = %v, want %v", m.Keys(), want)
	}
	if _, ok := m.Get("b"); ok {
		t.Error("b is still readable after Delete")
	}
	if m.Len() != 2 {
		t.Errorf("len = %d, want 2", m.Len())
	}

	// Re-setting a deleted key appends it, rather than restoring its old slot.
	m.Set("b", 9)
	if want := []string{"a", "c", "b"}; !reflect.DeepEqual(m.Keys(), want) {
		t.Errorf("keys = %v, want %v", m.Keys(), want)
	}
}

func TestOrderedMapDeleteToleratesMissingKeysAndNil(t *testing.T) {
	m := NewOrderedMap()
	m.Set("a", 1)
	m.Delete("absent")
	if m.Len() != 1 {
		t.Errorf("len = %d, want 1: deleting an absent key changed the map", m.Len())
	}

	var nilMap *OrderedMap
	nilMap.Delete("a") // must not panic
	if nilMap.Len() != 0 || nilMap.Keys() != nil {
		t.Error("a nil map stopped reading as empty")
	}
	if _, ok := nilMap.Get("a"); ok {
		t.Error("a nil map returned a value")
	}
}

// A meta value like `1000000` must not come back as 1e+06, and a genuine float
// must not be flattened to an integer.
func TestNumbersRoundTripAsWritten(t *testing.T) {
	const in = `{"big":1000000,"neg":-42,"pi":3.5,"huge":123456789012345678901234567890,
	             "arr":[1,2.5,[3]],"obj":{"n":7,"deep":{"m":8}},"str":"9","bool":true,"null":null}`
	var m OrderedMap
	if err := json.Unmarshal([]byte(in), &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	cases := []struct {
		key  string
		want any
	}{
		{"big", int64(1000000)},
		{"neg", int64(-42)},
		{"pi", 3.5},
		{"str", "9"},
		{"bool", true},
		{"null", nil},
	}
	for _, c := range cases {
		got, ok := m.Get(c.key)
		if !ok {
			t.Errorf("%s is missing", c.key)
			continue
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s = %#v, want %#v", c.key, got, c.want)
		}
	}

	// Too wide for an int64, but still finite: it stays a float rather than
	// becoming a json.Number nobody downstream knows how to render.
	huge, _ := m.Get("huge")
	f, ok := huge.(float64)
	if !ok || math.IsInf(f, 0) {
		t.Errorf("huge = %#v, want a finite float64", huge)
	}

	// Normalisation has to reach inside arrays and maps, at every depth.
	arr, _ := m.Get("arr")
	wantArr := []any{int64(1), 2.5, []any{int64(3)}}
	if !reflect.DeepEqual(arr, wantArr) {
		t.Errorf("arr = %#v, want %#v", arr, wantArr)
	}
	obj, _ := m.Get("obj")
	wantObj := map[string]any{"n": int64(7), "deep": map[string]any{"m": int64(8)}}
	if !reflect.DeepEqual(obj, wantObj) {
		t.Errorf("obj = %#v, want %#v", obj, wantObj)
	}
}

func TestUnionTagsKeepsFirstSeenOrderAndDropsDuplicates(t *testing.T) {
	cases := []struct {
		name      string
		have, add []string
		want      []string
	}{
		{"nothing to add returns the original", []string{"a"}, nil, []string{"a"}},
		{"appends what is new", []string{"a"}, []string{"b"}, []string{"a", "b"}},
		{"drops a duplicate", []string{"a", "b"}, []string{"b", "c"}, []string{"a", "b", "c"}},
		{"dedupes within have", []string{"a", "a"}, []string{"a"}, []string{"a"}},
		{"adds onto nothing", nil, []string{"b", "b", "a"}, []string{"b", "a"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := UnionTags(c.have, c.add); !reflect.DeepEqual(got, c.want) {
				t.Errorf("UnionTags(%v, %v) = %v, want %v", c.have, c.add, got, c.want)
			}
		})
	}
}
