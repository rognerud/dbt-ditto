package dbt

import (
	"bytes"
	"encoding/json"
	"fmt"

	"gopkg.in/yaml.v3"
)

// OrderedMap is a JSON object that remembers the order its keys were written
// in. dbt writes a column's `meta` in the order the analyst typed it into the
// YAML, and merging inherited meta has to preserve that order to produce the
// same file back, so the plain map[string]any that encoding/json would give us
// is not enough.
type OrderedMap struct {
	keys   []string
	values map[string]any
}

// MarshalYAML renders the map as a YAML mapping in key order.
//
// Without this the struct's fields are unexported, so yaml.v3 writes `{}` — an
// empty map where a nested meta value should be. That is the failure mode a
// nested value hits first: it appears, it is empty, and nothing errors.
func (m *OrderedMap) MarshalYAML() (any, error) {
	n := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	if m == nil {
		return n, nil
	}
	for _, k := range m.keys {
		key := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: k}
		var val yaml.Node
		if err := val.Encode(m.values[k]); err != nil {
			return nil, err
		}
		n.Content = append(n.Content, key, &val)
	}
	return n, nil
}

// NewOrderedMap returns an empty map.
func NewOrderedMap() *OrderedMap {
	return &OrderedMap{values: map[string]any{}}
}

// Len returns the number of entries.
func (m *OrderedMap) Len() int {
	if m == nil {
		return 0
	}
	return len(m.keys)
}

// Keys returns the keys in insertion order.
func (m *OrderedMap) Keys() []string {
	if m == nil {
		return nil
	}
	return m.keys
}

// Get returns the value for key.
func (m *OrderedMap) Get(key string) (any, bool) {
	if m == nil {
		return nil, false
	}
	v, ok := m.values[key]
	return v, ok
}

// Set assigns key, appending it if new and leaving its position alone if not.
func (m *OrderedMap) Set(key string, val any) {
	if m.values == nil {
		m.values = map[string]any{}
	}
	if _, exists := m.values[key]; !exists {
		m.keys = append(m.keys, key)
	}
	m.values[key] = val
}

// Delete removes key.
func (m *OrderedMap) Delete(key string) {
	if m == nil {
		return
	}
	if _, exists := m.values[key]; !exists {
		return
	}
	delete(m.values, key)
	for i, k := range m.keys {
		if k == key {
			m.keys = append(m.keys[:i], m.keys[i+1:]...)
			break
		}
	}
}

// Clone returns a shallow copy.
func (m *OrderedMap) Clone() *OrderedMap {
	out := NewOrderedMap()
	if m == nil {
		return out
	}
	out.keys = append(out.keys, m.keys...)
	for k, v := range m.values {
		out.values[k] = v
	}
	return out
}

// UnmarshalJSON decodes a JSON object, recording key order.
func (m *OrderedMap) UnmarshalJSON(data []byte) error {
	m.keys = nil
	m.values = map[string]any{}
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		return nil
	}

	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	if delim, ok := tok.(json.Delim); !ok || delim != '{' {
		return fmt.Errorf("expected a JSON object, got %v", tok)
	}
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return err
		}
		key, ok := keyTok.(string)
		if !ok {
			return fmt.Errorf("expected an object key, got %v", keyTok)
		}
		var val any
		if err := dec.Decode(&val); err != nil {
			return err
		}
		m.Set(key, normaliseNumbers(val))
	}
	_, err = dec.Token() // closing brace
	return err
}

// MarshalJSON re-encodes in key order.
func (m *OrderedMap) MarshalJSON() ([]byte, error) {
	if m == nil {
		return []byte("null"), nil
	}
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, k := range m.keys {
		if i > 0 {
			buf.WriteByte(',')
		}
		key, err := json.Marshal(k)
		if err != nil {
			return nil, err
		}
		buf.Write(key)
		buf.WriteByte(':')
		val, err := json.Marshal(m.values[k])
		if err != nil {
			return nil, err
		}
		buf.Write(val)
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

// normaliseNumbers turns json.Number into int64 where it is integral and
// float64 otherwise, so that a meta value round-trips through YAML as the
// number the analyst wrote rather than as 1e+06.
func normaliseNumbers(v any) any {
	switch t := v.(type) {
	case json.Number:
		if i, err := t.Int64(); err == nil {
			return i
		}
		if f, err := t.Float64(); err == nil {
			return f
		}
		return t.String()
	case []any:
		for i := range t {
			t[i] = normaliseNumbers(t[i])
		}
		return t
	case map[string]any:
		for k := range t {
			t[k] = normaliseNumbers(t[k])
		}
		return t
	default:
		return v
	}
}

// UnionTags appends add to have, dropping duplicates and keeping first-seen
// order.
func UnionTags(have, add []string) []string {
	if len(add) == 0 {
		return have
	}
	seen := make(map[string]bool, len(have)+len(add))
	out := make([]string, 0, len(have)+len(add))
	for _, t := range append(append([]string(nil), have...), add...) {
		if !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	return out
}
