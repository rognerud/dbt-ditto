package dbt

import (
	"bufio"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// CatalogColumn is one column as reported by `dbt docs generate`.
type CatalogColumn struct {
	Type    string `json:"type"`
	Index   int    `json:"index"`
	Name    string `json:"name"`
	Comment string `json:"comment"`
}

// CatalogMetadata identifies the relation a catalog entry describes.
type CatalogMetadata struct {
	Name     string `json:"name"`
	Schema   string `json:"schema"`
	Database string `json:"database"`
}

// CatalogNode is a relation entry in catalog.json.
type CatalogNode struct {
	Metadata CatalogMetadata          `json:"metadata"`
	Columns  map[string]CatalogColumn `json:"columns"`

	ordered     []CatalogColumn
	orderedOnce sync.Once
	folded      map[string]CatalogColumn
	foldedOnce  sync.Once
}

// Folded indexes the entry's columns by lower-cased name.
func (n *CatalogNode) Folded() map[string]CatalogColumn {
	n.foldedOnce.Do(func() {
		idx := make(map[string]CatalogColumn, len(n.Columns))
		for k, c := range n.Columns {
			idx[strings.ToLower(k)] = c
		}
		n.folded = idx
	})
	return n.folded
}

// Catalog is a decoded catalog.json.
type Catalog struct {
	Nodes   map[string]*CatalogNode `json:"nodes"`
	Sources map[string]*CatalogNode `json:"sources"`

	byRelation     map[string]*CatalogNode
	byRelationOnce sync.Once
}

// LoadCatalog decodes catalog.json from path.
func LoadCatalog(path string) (*Catalog, error) {
	r, closer, err := openArtifact(path)
	if err != nil {
		return nil, fmt.Errorf("open catalog: %w", err)
	}
	defer closer()

	c := &Catalog{}
	if err := json.NewDecoder(bufio.NewReaderSize(r, 1<<20)).Decode(c); err != nil {
		return nil, fmt.Errorf("decode catalog %s: %w", path, err)
	}
	return c, nil
}

// Lookup returns the catalog entry describing a node: unique_id first, then the
// relation matched on schema and name, which is how dbt-osmosis finds an entry
// and what makes another project's catalog usable.
func (c *Catalog) Lookup(n *Node) (*CatalogNode, bool) {
	if c == nil || n == nil {
		return nil, false
	}
	if e, ok := c.Nodes[n.UniqueID]; ok {
		return e, true
	}
	if e, ok := c.Sources[n.UniqueID]; ok {
		return e, true
	}
	c.byRelationOnce.Do(func() {
		c.byRelation = make(map[string]*CatalogNode, len(c.Nodes)+len(c.Sources))
		for _, set := range []map[string]*CatalogNode{c.Nodes, c.Sources} {
			for _, e := range set {
				c.byRelation[relationKey(e.Metadata.Schema, e.Metadata.Name)] = e
			}
		}
	})
	e, ok := c.byRelation[relationKey(n.Schema, n.Relation())]
	return e, ok
}

func relationKey(schema, name string) string {
	return strings.ToLower(schema) + "." + strings.ToLower(name)
}

// Invalidate drops the relation index, so an entry added after the first Lookup is
// still found by schema and name.
func (c *Catalog) Invalidate() {
	if c == nil {
		return
	}
	c.byRelationOnce = sync.Once{}
	c.byRelation = nil
}

// Ordered returns the catalog columns sorted by their warehouse ordinal.
func (n *CatalogNode) Ordered() []CatalogColumn {
	n.orderedOnce.Do(func() {
		out := make([]CatalogColumn, 0, len(n.Columns))
		for _, c := range n.Columns {
			out = append(out, c)
		}
		sort.Slice(out, func(i, j int) bool {
			if out[i].Index != out[j].Index {
				return out[i].Index < out[j].Index
			}
			return out[i].Name < out[j].Name
		})
		n.ordered = out
	})
	return n.ordered
}
