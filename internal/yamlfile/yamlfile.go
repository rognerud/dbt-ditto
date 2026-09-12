// Package yamlfile edits dbt schema YAML in place through yaml.Node, so that
// comments, key order and formatting survive a round trip.
package yamlfile

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// File is a loaded (or newly created) schema YAML document.
type File struct {
	Path    string
	Doc     *yaml.Node
	Created bool

	original []byte
	// loadedPrint fingerprints the document as it was read. Serialising YAML is
	// expensive, so a run that ends up leaving a file exactly as it found it —
	// which is what `--check` does on a healthy repository, and most of what any
	// re-run does — compares fingerprints instead of rendering.
	loadedPrint uint64
}

// Load reads a schema YAML file. An empty or whitespace-only file is treated as
// a fresh document rather than an error.
func Load(path string) (*File, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	f := &File{Path: path, original: raw}
	if len(bytes.TrimSpace(raw)) == 0 {
		f.Doc = newDoc()
		f.loadedPrint = fingerprint(f.Doc)
		return f, nil
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, fmt.Errorf("parse %s: expected a top-level mapping", path)
	}
	f.Doc = &doc
	f.loadedPrint = fingerprint(f.Doc)
	return f, nil
}

// New returns an empty `version: 2` document destined for path.
func New(path string) *File {
	return &File{Path: path, Doc: newDoc(), Created: true}
}

// LoadOrNew loads path if it exists and creates an empty document otherwise.
func LoadOrNew(path string) (*File, error) {
	f, err := Load(path)
	if os.IsNotExist(err) {
		return New(path), nil
	}
	return f, err
}

func newDoc() *yaml.Node {
	root := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	MapSet(root, "version", Scalar("2"))
	// version is an int in dbt schema files.
	MapGet(root, "version").Tag = "!!int"
	return &yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{root}}
}

// Root returns the top-level mapping node.
func (f *File) Root() *yaml.Node { return f.Doc.Content[0] }

// Render serialises the document.
func (f *File) Render() ([]byte, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(f.Doc); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Changed reports whether rendering the document would alter the file on disk.
// It returns the rendered bytes when there is a change, and nil when there is
// not: an unmodified document is recognised by its fingerprint and never
// rendered at all.
func (f *File) Changed() (bool, []byte, error) {
	if !f.Created && fingerprint(f.Doc) == f.loadedPrint {
		return false, nil, nil
	}
	out, err := f.Render()
	if err != nil {
		return false, nil, err
	}
	return !bytes.Equal(out, f.original), out, nil
}

// fingerprint hashes everything about a document that affects how it is
// written out, so an untouched document can be recognised without serialising
// it. Comments are included because moving one changes the file.
//
// It is a hand-rolled FNV-1a rather than hash/fnv because the standard
// interface takes []byte: hashing a node's six strings through it would convert
// each one, and a fingerprint is taken for every file both before and after the
// run.
func fingerprint(n *yaml.Node) uint64 {
	h := uint64(fnvOffset)
	hashNode(&h, n)
	return h
}

const (
	fnvOffset = 14695981039346656037
	fnvPrime  = 1099511628211
)

func hashByte(h *uint64, b byte) {
	*h ^= uint64(b)
	*h *= fnvPrime
}

func hashString(h *uint64, s string) {
	for i := 0; i < len(s); i++ {
		hashByte(h, s[i])
	}
	hashByte(h, 0x1f) // separator, so "ab"+"c" and "a"+"bc" differ
}

func hashUint(h *uint64, v uint64) {
	for i := 0; i < 8; i++ {
		hashByte(h, byte(v))
		v >>= 8
	}
}

func hashNode(h *uint64, n *yaml.Node) {
	if n == nil {
		hashByte(h, 0)
		return
	}
	hashUint(h, uint64(n.Kind))
	hashUint(h, uint64(n.Style))
	hashString(h, n.Tag)
	hashString(h, n.Value)
	hashString(h, n.Anchor)
	hashString(h, n.HeadComment)
	hashString(h, n.LineComment)
	hashString(h, n.FootComment)
	hashUint(h, uint64(len(n.Content)))
	for _, c := range n.Content {
		hashNode(h, c)
	}
}

// Save writes the document if it differs from what is on disk. It returns
// whether a write was needed, so callers can report and `--check` can fail.
func (f *File) Save(dryRun bool) (bool, error) {
	changed, out, err := f.Changed()
	if err != nil || !changed {
		return false, err
	}
	if dryRun {
		return true, nil
	}
	if err := os.MkdirAll(filepath.Dir(f.Path), 0o755); err != nil {
		return false, err
	}
	if err := os.WriteFile(f.Path, out, 0o644); err != nil {
		return false, err
	}
	f.original = out
	f.Created = false
	f.loadedPrint = fingerprint(f.Doc)
	return true, nil
}

// IsEmpty reports whether the document carries no dbt content, meaning the file
// can be deleted after its last entry has been moved elsewhere.
func (f *File) IsEmpty() bool {
	root := f.Root()
	for i := 0; i+1 < len(root.Content); i += 2 {
		key := root.Content[i].Value
		if key == "version" {
			continue
		}
		val := root.Content[i+1]
		if val.Kind == yaml.SequenceNode && len(val.Content) == 0 {
			continue
		}
		if val.Kind == yaml.ScalarNode && val.Value == "" {
			continue
		}
		return false
	}
	return true
}

// Seq returns the sequence node stored under key, creating it when create is
// set. It returns nil if the key is absent (or not a sequence) and create is
// false.
func (f *File) Seq(key string, create bool) *yaml.Node {
	root := f.Root()
	if n := MapGet(root, key); n != nil {
		if n.Kind == yaml.SequenceNode {
			return n
		}
		if !create {
			return nil
		}
		MapDelete(root, key)
	}
	if !create {
		return nil
	}
	seq := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
	MapSet(root, key, seq)
	return seq
}

// Entry finds the mapping in the named sequence whose `name` matches, creating
// it when create is set.
func (f *File) Entry(seqKey, name string, create bool) *yaml.Node {
	seq := f.Seq(seqKey, create)
	if seq == nil {
		return nil
	}
	if e := findByName(seq, name); e != nil {
		return e
	}
	if !create {
		return nil
	}
	entry := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	MapSet(entry, "name", Scalar(name))
	seq.Content = append(seq.Content, entry)
	return entry
}

// RemoveEntry drops the named entry from a sequence and reports whether it was
// present.
func (f *File) RemoveEntry(seqKey, name string) bool {
	seq := f.Seq(seqKey, false)
	if seq == nil {
		return false
	}
	for i, e := range seq.Content {
		if e.Kind == yaml.MappingNode && StringOf(MapGet(e, "name")) == name {
			seq.Content = append(seq.Content[:i], seq.Content[i+1:]...)
			if len(seq.Content) == 0 {
				MapDelete(f.Root(), seqKey)
			}
			return true
		}
	}
	return false
}

// SourceTable locates `sources[name=source].tables[name=table]`, creating the
// source and table entries when create is set.
func (f *File) SourceTable(source, table string, create bool) *yaml.Node {
	src := f.Entry("sources", source, create)
	if src == nil {
		return nil
	}
	tables := MapGet(src, "tables")
	if tables == nil || tables.Kind != yaml.SequenceNode {
		if !create {
			return nil
		}
		tables = &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		MapSet(src, "tables", tables)
	}
	if e := findByName(tables, table); e != nil {
		return e
	}
	if !create {
		return nil
	}
	entry := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	MapSet(entry, "name", Scalar(table))
	tables.Content = append(tables.Content, entry)
	return entry
}

func findByName(seq *yaml.Node, name string) *yaml.Node {
	for _, e := range seq.Content {
		if e.Kind == yaml.MappingNode && StringOf(MapGet(e, "name")) == name {
			return e
		}
	}
	return nil
}

// MapGet returns the value node for key in a mapping, or nil.
func MapGet(m *yaml.Node, key string) *yaml.Node {
	if m == nil || m.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

// MapSet assigns key in a mapping, replacing the value in place if the key
// already exists so that key order and any attached comments are preserved.
func MapSet(m *yaml.Node, key string, val *yaml.Node) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			val.HeadComment = m.Content[i+1].HeadComment
			val.LineComment = m.Content[i+1].LineComment
			val.FootComment = m.Content[i+1].FootComment
			m.Content[i+1] = val
			return
		}
	}
	m.Content = append(m.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, val)
}

// MapDelete removes key from a mapping and reports whether it was present.
func MapDelete(m *yaml.Node, key string) bool {
	if m == nil || m.Kind != yaml.MappingNode {
		return false
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			m.Content = append(m.Content[:i], m.Content[i+2:]...)
			return true
		}
	}
	return false
}

// Scalar builds a string scalar, using a literal block for multi-line text so
// long descriptions stay readable.
func Scalar(s string) *yaml.Node {
	n := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: s}
	if strings.Contains(s, "\n") {
		n.Style = yaml.LiteralStyle
	}
	return n
}

// StringOf reads a scalar node's value, tolerating nil.
func StringOf(n *yaml.Node) string {
	if n == nil || n.Kind != yaml.ScalarNode {
		return ""
	}
	return n.Value
}

// Encode converts an arbitrary Go value into a yaml.Node.
//
// The scalar cases are built by hand rather than handed to yaml.Node.Encode.
// That method stands up a complete YAML emitter and parser for every value it
// is given, which costs on the order of a hundred kilobytes a call; meta values
// are almost all scalars and there is one per key per column, so going through
// it dominated both allocation and runtime on a large project.
func Encode(v any) (*yaml.Node, error) {
	switch t := v.(type) {
	case nil:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!null", Value: "null"}, nil
	case string:
		return Scalar(t), nil
	case bool:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: strconv.FormatBool(t)}, nil
	case int:
		return intNode(int64(t)), nil
	case int32:
		return intNode(int64(t)), nil
	case int64:
		return intNode(t), nil
	case uint:
		return intNode(int64(t)), nil
	case uint64:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: strconv.FormatUint(t, 10)}, nil
	case float32:
		return floatNode(float64(t)), nil
	case float64:
		return floatNode(t), nil
	}

	var n yaml.Node
	if err := n.Encode(v); err != nil {
		return nil, err
	}
	return &n, nil
}

func intNode(v int64) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: strconv.FormatInt(v, 10)}
}

func floatNode(v float64) *yaml.Node {
	s := strconv.FormatFloat(v, 'g', -1, 64)
	// A float that formats without a marker would read back as an integer, so
	// give it one.
	if !strings.ContainsAny(s, ".eEnN") {
		s += ".0"
	}
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!float", Value: s}
}

// DecodeStrings reads a sequence node back into a string slice.
func DecodeStrings(n *yaml.Node) []string {
	if n == nil || n.Kind != yaml.SequenceNode {
		return nil
	}
	var s []string
	if err := n.Decode(&s); err != nil {
		return nil
	}
	return s
}
