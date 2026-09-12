// Package dbt reads the dbt artifacts dbt-ditto operates on: manifest.json,
// catalog.json and dbt_project.yml. Only the fields inheritance needs are
// decoded; everything else is skipped by encoding/json.
package dbt

import (
	"bufio"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

// ColumnConfig is the `config:` block dbt >= 1.9.6 allows on a column. Meta and
// tags written there are equivalent to the top-level ones and dbt merges them,
// so both have to be read.
type ColumnConfig struct {
	Meta *OrderedMap `json:"meta"`
	Tags []string    `json:"tags"`
}

// Column is a column entry as it appears on a node in manifest.json.
type Column struct {
	Name        string        `json:"name"`
	Description string        `json:"description"`
	Meta        *OrderedMap   `json:"meta"`
	Tags        []string      `json:"tags"`
	DataType    string        `json:"data_type"`
	Config      *ColumnConfig `json:"config"`

	// Extra holds column keys dbt-ditto does not model, decoded only when
	// ExtraColumnKeys names them. `policy_tags` is the motivating case: it is
	// dbt's own key, it is meaningful to inherit, and nothing here should have
	// to understand what it means to carry it downstream.
	Extra map[string]any `json:"-"`
}

// ExtraColumnKeys names the column keys to decode into Column.Extra.
//
// It is a package-level list rather than a parameter because decoding runs
// through encoding/json, which gives an UnmarshalJSON method no way to be told
// anything. Set it once before any LoadManifest call and never during one: the
// loader reads projects in parallel.
//
// Empty is the fast path, and the default. Capturing every unknown key instead
// would allocate a map per column on a document with millions of them, to carry
// fields nobody asked to propagate.
var ExtraColumnKeys []string

// columnFields exists to give json a struct to decode into without recursing
// back into Column.UnmarshalJSON.
type columnFields Column

// UnmarshalJSON decodes the modelled fields, then the named extras.
func (c *Column) UnmarshalJSON(b []byte) error {
	if err := json.Unmarshal(b, (*columnFields)(c)); err != nil {
		return err
	}
	if len(ExtraColumnKeys) == 0 {
		return nil
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	for _, key := range ExtraColumnKeys {
		msg, ok := raw[key]
		if !ok {
			continue
		}
		var v any
		if err := json.Unmarshal(msg, &v); err != nil {
			continue // a key we do not understand is not worth failing a run over
		}
		if v == nil {
			continue
		}
		if c.Extra == nil {
			c.Extra = make(map[string]any, len(ExtraColumnKeys))
		}
		c.Extra[key] = v
	}
	return nil
}

// EffectiveMeta merges the column's top-level meta with its `config.meta`,
// which is where dbt >= 1.9.6 records meta written in the newer YAML shape.
// Top-level keys come first, matching the order dbt itself reports.
func (c *Column) EffectiveMeta() *OrderedMap {
	if c == nil {
		return nil
	}
	if c.Config == nil || c.Config.Meta.Len() == 0 {
		return c.Meta
	}
	out := c.Meta.Clone()
	for _, k := range c.Config.Meta.Keys() {
		v, _ := c.Config.Meta.Get(k)
		out.Set(k, v)
	}
	return out
}

// EffectiveTags is the union of the column's top-level tags and `config.tags`.
func (c *Column) EffectiveTags() []string {
	if c == nil {
		return nil
	}
	if c.Config == nil {
		return c.Tags
	}
	return UnionTags(c.Tags, c.Config.Tags)
}

// DependsOn holds the upstream unique_ids of a node.
type DependsOn struct {
	Nodes []string `json:"nodes"`
}

// Node covers both `nodes` and `sources` entries. Source-only fields
// (SourceName, Identifier) are empty for models.
type Node struct {
	UniqueID         string             `json:"unique_id"`
	Name             string             `json:"name"`
	ResourceType     string             `json:"resource_type"`
	PackageName      string             `json:"package_name"`
	Database         string             `json:"database"`
	Schema           string             `json:"schema"`
	Alias            string             `json:"alias"`
	Identifier       string             `json:"identifier"`
	SourceName       string             `json:"source_name"`
	RelationName     string             `json:"relation_name"`
	OriginalFilePath string             `json:"original_file_path"`
	PatchPath        string             `json:"patch_path"`
	Path             string             `json:"path"`
	FQN              []string           `json:"fqn"`
	Description      string             `json:"description"`
	Columns          map[string]*Column `json:"columns"`
	Meta             map[string]any     `json:"meta"`
	Tags             []string           `json:"tags"`
	Access           string             `json:"access"`
	DependsOn        DependsOn          `json:"depends_on"`
	Config           map[string]any     `json:"config"`

	// Populated by the loader, not present in the artifact.
	Project  *Project  `json:"-"`
	Manifest *Manifest `json:"-"`
	// Order is the node's position in the manifest. dbt writes YAML entries in
	// this order, so keeping it lets dbt-ditto lay out a schema file the same
	// way rather than in some order of its own.
	Order int `json:"-"`

	// colIndex maps a lower-cased column name to its entry. Inheritance looks a
	// column up once per ancestor per column, so a linear scan over Columns here
	// turns the resolve step quadratic on wide models.
	colIndex     map[string]*Column
	colIndexOnce sync.Once
}

// Column returns the node's column with the given name, matched case
// insensitively when fold is set.
func (n *Node) Column(name string, fold bool) *Column {
	if n == nil || len(n.Columns) == 0 {
		return nil
	}
	if !fold {
		return n.Columns[name]
	}
	n.colIndexOnce.Do(func() {
		idx := make(map[string]*Column, len(n.Columns))
		for k, c := range n.Columns {
			// An exact key always wins over a case variant, so insert the
			// folded key only when it is free or the exact spelling matches.
			lk := strings.ToLower(k)
			if _, taken := idx[lk]; !taken || k == lk {
				idx[lk] = c
			}
		}
		n.colIndex = idx
	})
	if c, ok := n.Columns[name]; ok {
		return c
	}
	return n.colIndex[strings.ToLower(name)]
}

// emptyColumn stands in for a column the warehouse has but no YAML documents.
// It carries no knowledge, but its existence still matters: an ancestor that
// has the column claims it for its generation whether or not it is documented.
var emptyColumn = &Column{}

// EffectiveColumn is Column, widened to the columns the warehouse reports for
// the node even when nothing documents them yet.
//
// dbt-osmosis injects every catalog column into its in-memory manifest before
// inheritance runs, so an undocumented intermediate model still shadows its own
// ancestors. Consulting the catalog here reproduces that without mutating
// anything.
func (n *Node) EffectiveColumn(name string, fold bool) *Column {
	if c := n.Column(name, fold); c != nil {
		return c
	}
	if n.Project == nil || n.Project.Catalog == nil {
		return nil
	}
	entry, ok := n.Project.Catalog.Lookup(n)
	if !ok {
		return nil
	}
	if _, ok := entry.Columns[name]; ok {
		return emptyColumn
	}
	if !fold {
		return nil
	}
	if _, ok := entry.Folded()[strings.ToLower(name)]; ok {
		return emptyColumn
	}
	return nil
}

// InvalidateColumnIndex drops the folded column index, so a column added after
// the first lookup is still found. Columns are only ever added before
// resolution starts, by a source provider describing a relation the YAML does
// not mention yet.
func (n *Node) InvalidateColumnIndex() {
	n.colIndexOnce = sync.Once{}
	n.colIndex = nil
}

// IsSource reports whether the node came from the manifest's `sources` map.
func (n *Node) IsSource() bool { return n.ResourceType == "source" }

// Relation returns the identifier the node materializes as.
func (n *Node) Relation() string {
	if n.Identifier != "" {
		return n.Identifier
	}
	if n.Alias != "" {
		return n.Alias
	}
	return n.Name
}

// ConfigMeta returns the node's `config.meta`, which dbt merges into Meta but
// which we also read directly so `+meta:` set in dbt_project.yml is visible.
func (n *Node) ConfigMeta() map[string]any {
	if n.Config == nil {
		return nil
	}
	m, _ := n.Config["meta"].(map[string]any)
	return m
}

// ConfigString reads a string-valued key out of the node config. dbt places
// unrecognised `+key:` entries from dbt_project.yml straight into config, which
// is how the per-model YAML path rule is threaded through.
func (n *Node) ConfigString(key string) (string, bool) {
	if n.Config == nil {
		return "", false
	}
	v, ok := n.Config[key]
	if !ok {
		return "", false
	}
	s, ok := v.(string)
	return s, ok
}

// Metadata is the manifest header.
type Metadata struct {
	ProjectName   string `json:"project_name"`
	AdapterType   string `json:"adapter_type"`
	DbtVersion    string `json:"dbt_version"`
	DbtSchemaVers string `json:"dbt_schema_version"`
}

// Manifest is a decoded manifest.json.
type Manifest struct {
	Metadata Metadata         `json:"metadata"`
	Nodes    map[string]*Node `json:"nodes"`
	Sources  map[string]*Node `json:"sources"`

	Path string `json:"-"`
}

// LoadManifest decodes manifest.json (optionally gzipped) from path.
func LoadManifest(path string) (*Manifest, error) {
	r, closer, err := openArtifact(path)
	if err != nil {
		return nil, fmt.Errorf("open manifest: %w", err)
	}
	defer closer()

	m := &Manifest{Path: path, Nodes: map[string]*Node{}, Sources: map[string]*Node{}}
	dec := json.NewDecoder(bufio.NewReaderSize(r, 1<<20))
	if err := m.decode(dec); err != nil {
		return nil, fmt.Errorf("decode manifest %s: %w", path, err)
	}
	return m, nil
}

// decode reads a manifest as a stream.
//
// A real manifest is mostly things dbt-ditto has no use for: compiled SQL,
// macros, the child and parent maps, every disabled node. Walking the document
// with a token decoder and skipping those keys outright, rather than handing the
// whole file to reflection-based decoding, is the difference between reading a
// large manifest in a moment and reading it in several seconds. Streaming also
// preserves the order nodes appear in, which is the order dbt-osmosis writes
// them back out in.
func (m *Manifest) decode(dec *json.Decoder) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return fmt.Errorf("expected a JSON object at the top level, got %v", tok)
	}

	order := 0
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return err
		}
		key, _ := keyTok.(string)
		switch key {
		case "metadata":
			if err := dec.Decode(&m.Metadata); err != nil {
				return err
			}
		case "nodes":
			if err := decodeNodeMap(dec, m.Nodes, m, &order, ""); err != nil {
				return err
			}
		case "sources":
			if err := decodeNodeMap(dec, m.Sources, m, &order, "source"); err != nil {
				return err
			}
		default:
			if err := skipValue(dec); err != nil {
				return err
			}
		}
	}
	_, err = dec.Token() // closing brace
	return err
}

// decodeNodeMap reads one `{unique_id: node}` object.
func decodeNodeMap(dec *json.Decoder, into map[string]*Node, m *Manifest, order *int, forceType string) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return fmt.Errorf("expected an object of nodes, got %v", tok)
	}
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return err
		}
		id, _ := keyTok.(string)
		n := &Node{}
		if err := dec.Decode(n); err != nil {
			return fmt.Errorf("node %s: %w", id, err)
		}
		n.UniqueID = id
		n.Manifest = m
		n.Order = *order
		*order++
		if forceType != "" {
			n.ResourceType = forceType
		}
		into[id] = n
	}
	_, err = dec.Token() // closing brace
	return err
}

// skipValue consumes the next value without materialising it.
func skipValue(dec *json.Decoder) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	delim, ok := tok.(json.Delim)
	if !ok || (delim != '{' && delim != '[') {
		return nil
	}
	depth := 1
	for depth > 0 {
		tok, err := dec.Token()
		if err != nil {
			return err
		}
		if d, ok := tok.(json.Delim); ok {
			switch d {
			case '{', '[':
				depth++
			case '}', ']':
				depth--
			}
		}
	}
	return nil
}

// SchemaFile returns the repo-relative path of the YAML file that currently
// documents the node, and whether one exists. Models carry a patch_path of the
// form `package://models/schema.yml`; sources are defined in their own file.
func (n *Node) SchemaFile() (string, bool) {
	if n.IsSource() {
		if n.OriginalFilePath == "" {
			return "", false
		}
		return n.OriginalFilePath, true
	}
	if n.PatchPath == "" {
		return "", false
	}
	p := n.PatchPath
	if i := strings.Index(p, "://"); i >= 0 {
		p = p[i+3:]
	}
	return filepath.ToSlash(p), true
}

// openArtifact opens a dbt artifact, transparently decompressing a `.gz` one.
// Both the manifest and the catalog go through it: dbt Cloud serves artifacts
// gzipped, and an artifact checked into a repository is worth compressing.
//
// The returned close function releases the gzip reader and the file together.
func openArtifact(path string) (io.Reader, func(), error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, func() {}, err
	}
	if !strings.HasSuffix(path, ".gz") {
		return f, func() { _ = f.Close() }, nil
	}

	gz, err := gzip.NewReader(f)
	if err != nil {
		_ = f.Close()
		return nil, func() {}, fmt.Errorf("gunzip %s: %w", path, err)
	}
	return gz, func() {
		_ = gz.Close()
		_ = f.Close()
	}, nil
}

// configBlockMinVersion is the dbt release that introduced column-level
// `config:` blocks. dbt-osmosis switches its output shape at exactly this
// version, so dbt-ditto does too.
var configBlockMinVersion = [3]int{1, 9, 6}

// WantsConfigBlock reports whether column meta and tags should be nested under
// `config:`, inferred from the dbt version that produced the manifest. An
// unparseable version is treated as old, which is the safe direction: nesting
// under `config:` on a dbt that does not understand it loses the data silently.
func (m *Manifest) WantsConfigBlock() bool {
	v, ok := parseVersion(m.Metadata.DbtVersion)
	if !ok {
		return false
	}
	for i := 0; i < 3; i++ {
		if v[i] != configBlockMinVersion[i] {
			return v[i] > configBlockMinVersion[i]
		}
	}
	return true
}

// parseVersion reads a leading `major.minor.patch` out of a version string,
// ignoring any pre-release or build suffix.
func parseVersion(s string) ([3]int, bool) {
	var out [3]int
	for i := 0; i < 3; i++ {
		if s == "" {
			return out, i > 0
		}
		j := 0
		for j < len(s) && s[j] >= '0' && s[j] <= '9' {
			j++
		}
		if j == 0 {
			return out, i > 0
		}
		n, err := strconv.Atoi(s[:j])
		if err != nil {
			return out, false
		}
		out[i] = n
		s = s[j:]
		if strings.HasPrefix(s, ".") {
			s = s[1:]
		} else {
			return out, true
		}
	}
	return out, true
}

// Documentable reports whether the node is a kind dbt-ditto writes YAML for.
func (n *Node) Documentable() bool {
	switch n.ResourceType {
	case "model", "seed", "snapshot", "source":
		return true
	}
	return false
}
