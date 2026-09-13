package yamlfile

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// Meta values arrive as whatever JSON decoded to, and have to come back out of
// YAML as the same thing. The scalar cases are hand-built rather than handed to
// yaml.Node.Encode, so each one needs saying.
func TestEncodeScalars(t *testing.T) {
	cases := []struct {
		name string
		in   any
		tag  string
		want string
	}{
		{"nil", nil, "!!null", "null"},
		{"string", "hello", "!!str", "hello"},
		{"empty string", "", "!!str", ""},
		{"true", true, "!!bool", "true"},
		{"false", false, "!!bool", "false"},
		{"int", 42, "!!int", "42"},
		{"negative int", -42, "!!int", "-42"},
		{"int64", int64(1000000), "!!int", "1000000"},
		{"int8", int8(-8), "!!int", "-8"},
		{"uint", uint(7), "!!int", "7"},
		{"uint64", uint64(1 << 40), "!!int", "1099511627776"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			n, err := Encode(c.in)
			if err != nil {
				t.Fatalf("Encode(%#v): %v", c.in, err)
			}
			if n.Tag != c.tag {
				t.Errorf("tag = %q, want %q", n.Tag, c.tag)
			}
			if n.Value != c.want {
				t.Errorf("value = %q, want %q", n.Value, c.want)
			}
		})
	}
}

// A float that formats without a marker would read back as an integer, so it is
// given one. Without that, a meta value of 5.0 would silently become 5.
func TestEncodeFloatsStayFloats(t *testing.T) {
	cases := []struct {
		in   any
		want string
	}{
		{3.5, "3.5"},
		{float64(5), "5.0"},
		{float64(0), "0.0"},
		{-2.0, "-2.0"},
		{float32(1.5), "1.5"},
		{1e21, "1e+21"},
		{1e-7, "1e-07"},
		{0.1, "0.1"},
	}
	for _, c := range cases {
		n, err := Encode(c.in)
		if err != nil {
			t.Fatalf("Encode(%#v): %v", c.in, err)
		}
		if n.Tag != "!!float" {
			t.Errorf("Encode(%#v) tag = %q, want !!float", c.in, n.Tag)
		}
		if n.Value != c.want {
			t.Errorf("Encode(%#v) = %q, want %q", c.in, n.Value, c.want)
		}
	}
}

// The point of the marker: a whole-number float has to survive a round trip
// through YAML as a float rather than arriving back as an int.
func TestWholeNumberFloatSurvivesARoundTrip(t *testing.T) {
	n, err := Encode(float64(5))
	if err != nil {
		t.Fatal(err)
	}
	out, err := yaml.Marshal(n)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(out)); got != "5.0" {
		t.Fatalf("marshalled to %q, want 5.0", got)
	}

	var back any
	if err := yaml.Unmarshal(out, &back); err != nil {
		t.Fatal(err)
	}
	if _, ok := back.(float64); !ok {
		t.Errorf("read back as %T (%v), want float64", back, back)
	}
}

// Anything that is not a scalar falls through to yaml's own encoder.
func TestEncodeFallsBackForCompositeValues(t *testing.T) {
	n, err := Encode([]any{1, "two", 3.5})
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if n.Kind != yaml.SequenceNode {
		t.Fatalf("kind = %v, want a sequence", n.Kind)
	}
	if len(n.Content) != 3 {
		t.Errorf("got %d items, want 3", len(n.Content))
	}

	m, err := Encode(map[string]any{"a": 1})
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if m.Kind != yaml.MappingNode {
		t.Errorf("kind = %v, want a mapping", m.Kind)
	}
}

// Encode's error return is not asserted here: on a value it cannot render,
// yaml.Node.Encode panics rather than returning an error, so the branch is
// unreachable. Every value that gets here decoded out of JSON, so it is
// encodable by construction.

func TestStringOfToleratesWhatIsNotAScalar(t *testing.T) {
	if got := StringOf(nil); got != "" {
		t.Errorf("StringOf(nil) = %q, want empty", got)
	}
	if got := StringOf(&yaml.Node{Kind: yaml.MappingNode}); got != "" {
		t.Errorf("StringOf(mapping) = %q, want empty", got)
	}
	if got := StringOf(&yaml.Node{Kind: yaml.ScalarNode, Value: "x"}); got != "x" {
		t.Errorf("StringOf(scalar) = %q, want x", got)
	}
}

func TestDecodeStringsToleratesWhatIsNotASequence(t *testing.T) {
	if got := DecodeStrings(nil); got != nil {
		t.Errorf("DecodeStrings(nil) = %v, want nil", got)
	}
	if got := DecodeStrings(&yaml.Node{Kind: yaml.ScalarNode, Value: "x"}); got != nil {
		t.Errorf("DecodeStrings(scalar) = %v, want nil", got)
	}

	seq, err := Encode([]string{"a", "b"})
	if err != nil {
		t.Fatal(err)
	}
	if got := DecodeStrings(seq); len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Errorf("DecodeStrings = %v, want [a b]", got)
	}

	// A sequence of things that are not strings decodes to nothing rather than
	// failing a run.
	mixed, err := Encode([]any{map[string]any{"a": 1}})
	if err != nil {
		t.Fatal(err)
	}
	if got := DecodeStrings(mixed); got != nil {
		t.Errorf("DecodeStrings(sequence of mappings) = %v, want nil", got)
	}
}
