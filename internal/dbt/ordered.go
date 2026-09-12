package dbt

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"slices"

	"gopkg.in/yaml.v3"
)

// OrderedMap is a JSON object that remembers key order.
type OrderedMap struct {
	keys   []string
	values map[string]any
}

// MarshalYAML renders the map as a YAML mapping in key order.
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
	if i := slices.Index(m.keys, key); i >= 0 {
		m.keys = slices.Delete(m.keys, i, i+1)
	}
}

// Clone returns a shallow copy.
func (m *OrderedMap) Clone() *OrderedMap {
	out := NewOrderedMap()
	if m == nil {
		return out
	}
	out.keys = slices.Clone(m.keys)
	maps.Copy(out.values, m.values)
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
	if err := expectObject(dec, "a JSON object"); err != nil {
		return err
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
	_, err := dec.Token() // closing brace
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

// normaliseNumbers turns json.Number into int64 where integral and float64
// otherwise, so a meta value round-trips as written rather than as 1e+06.
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
func UnionTags(have, add []string) []string {
	if len(add) == 0 {
		return have
	}
	seen := make(map[string]bool, len(have)+len(add))
	out := make([]string, 0, len(have)+len(add))
	for _, t := range slices.Concat(have, add) {
		if !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	return out
}
