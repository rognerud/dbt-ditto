package dbt

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestOrderedMapKeepsKeyOrder(t *testing.T) {
	var m OrderedMap
	if err := json.Unmarshal([]byte(`{"zulu":1,"alpha":"a","mike":true}`), &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if want := []string{"zulu", "alpha", "mike"}; !reflect.DeepEqual(m.Keys(), want) {
		t.Errorf("keys = %v, want %v", m.Keys(), want)
	}
	if v, _ := m.Get("zulu"); v != int64(1) {
		t.Errorf("zulu = %#v, want the integer 1 rather than a float", v)
	}
}

func TestOrderedMapSetKeepsExistingPosition(t *testing.T) {
	m := NewOrderedMap()
	m.Set("a", 1)
	m.Set("b", 2)
	m.Set("a", 3)
	if want := []string{"a", "b"}; !reflect.DeepEqual(m.Keys(), want) {
		t.Errorf("keys = %v, want %v: re-setting a key must not move it", m.Keys(), want)
	}
	if v, _ := m.Get("a"); v != 3 {
		t.Errorf("a = %v, want 3", v)
	}
}

func TestOrderedMapCloneIsIndependent(t *testing.T) {
	m := NewOrderedMap()
	m.Set("a", 1)
	c := m.Clone()
	c.Set("b", 2)
	if m.Len() != 1 {
		t.Errorf("the original grew to %d entries when its clone was written to", m.Len())
	}
}

func TestOrderedMapHandlesNull(t *testing.T) {
	var m OrderedMap
	if err := json.Unmarshal([]byte(`null`), &m); err != nil {
		t.Fatalf("unmarshal null: %v", err)
	}
	if m.Len() != 0 {
		t.Errorf("len = %d, want 0", m.Len())
	}
}

func TestEffectiveMetaMergesConfigBlock(t *testing.T) {
	top := NewOrderedMap()
	top.Set("owner", "platform")
	top.Set("pii", false)
	cfg := NewOrderedMap()
	cfg.Set("pii", true)
	cfg.Set("tier", "gold")

	c := &Column{Meta: top, Config: &ColumnConfig{Meta: cfg}}
	got := c.EffectiveMeta()

	if want := []string{"owner", "pii", "tier"}; !reflect.DeepEqual(got.Keys(), want) {
		t.Errorf("keys = %v, want %v", got.Keys(), want)
	}
	if v, _ := got.Get("pii"); v != true {
		t.Errorf("pii = %v, want the config block to win", v)
	}
	if v, _ := top.Get("pii"); v != false {
		t.Error("EffectiveMeta mutated the column's own meta")
	}
}

func TestEffectiveTagsAreUnioned(t *testing.T) {
	c := &Column{Tags: []string{"pk", "core"}, Config: &ColumnConfig{Tags: []string{"core", "pii"}}}
	if want := []string{"pk", "core", "pii"}; !reflect.DeepEqual(c.EffectiveTags(), want) {
		t.Errorf("tags = %v, want %v", c.EffectiveTags(), want)
	}
}

func TestColumnLookupIsCaseInsensitiveButPrefersExact(t *testing.T) {
	exact := &Column{Name: "id"}
	upper := &Column{Name: "ID"}
	n := &Node{Columns: map[string]*Column{"id": exact, "ID": upper}}

	if got := n.Column("ID", true); got != upper {
		t.Error("an exact spelling must win over a case-folded match")
	}
	if got := n.Column("Id", true); got != exact {
		t.Error("a folded lookup must find the lower-cased column")
	}
	if got := n.Column("Id", false); got != nil {
		t.Error("a case-sensitive lookup must not match a different spelling")
	}
}

func TestWantsConfigBlockFollowsTheDbtVersion(t *testing.T) {
	cases := map[string]bool{
		"1.9.6":     true,
		"1.9.7":     true,
		"1.10.0":    true,
		"2.0.0":     true,
		"1.9.5":     false,
		"1.8.11":    false,
		"":          false,
		"not-a-ver": false,
		// A pre-release suffix should not change the decision.
		"1.10.0rc1": true,
	}
	for version, want := range cases {
		m := &Manifest{Metadata: Metadata{DbtVersion: version}}
		if got := m.WantsConfigBlock(); got != want {
			t.Errorf("dbt %q: config block = %v, want %v", version, got, want)
		}
	}
}

func TestSchemaFileStripsThePackagePrefix(t *testing.T) {
	n := &Node{PatchPath: "platform://models/staging/_stg_orders.yml"}
	got, ok := n.SchemaFile()
	if !ok || got != "models/staging/_stg_orders.yml" {
		t.Errorf("schema file = %q (ok=%v), want the path without the package prefix", got, ok)
	}

	src := &Node{ResourceType: "source", OriginalFilePath: "models/staging/_sources.yml"}
	if got, ok := src.SchemaFile(); !ok || got != "models/staging/_sources.yml" {
		t.Errorf("source schema file = %q (ok=%v)", got, ok)
	}

	if _, ok := (&Node{}).SchemaFile(); ok {
		t.Error("an undocumented node reported a schema file")
	}
}

func TestCatalogFallsBackToTheRelationName(t *testing.T) {
	entry := &CatalogNode{
		Metadata: CatalogMetadata{Name: "dim_customers", Schema: "MAIN"},
		Columns:  map[string]CatalogColumn{"id": {Name: "id"}},
	}
	c := &Catalog{Nodes: map[string]*CatalogNode{"model.other.dim_customers": entry}}

	n := &Node{UniqueID: "model.platform.dim_customers", Name: "dim_customers", Schema: "main"}
	if got, ok := c.Lookup(n); !ok || got != entry {
		t.Error("a catalog written by another project should still match on schema and name")
	}

	miss := &Node{UniqueID: "model.platform.absent", Name: "absent", Schema: "main"}
	if _, ok := c.Lookup(miss); ok {
		t.Error("matched a relation that is not in the catalog")
	}
}

func TestCatalogOrderedFollowsTheWarehouseOrdinal(t *testing.T) {
	n := &CatalogNode{Columns: map[string]CatalogColumn{
		"c": {Name: "c", Index: 2},
		"a": {Name: "a", Index: 0},
		"b": {Name: "b", Index: 1},
	}}
	var got []string
	for _, c := range n.Ordered() {
		got = append(got, c.Name)
	}
	if want := []string{"a", "b", "c"}; !reflect.DeepEqual(got, want) {
		t.Errorf("order = %v, want %v", got, want)
	}
}

// The streaming decoder has to ignore the bulk of a manifest, keep node order,
// and still populate the fields inheritance depends on.
func TestLoadManifestStreamsAndKeepsOrder(t *testing.T) {
	doc := `{
	  "metadata": {"project_name": "platform", "dbt_version": "1.10.0"},
	  "child_map": {"model.platform.a": ["model.platform.b"]},
	  "macros": {"macro.x": {"anything": [1, 2, {"deep": true}]}},
	  "nodes": {
	    "model.platform.zeta": {
	      "name": "zeta", "resource_type": "model", "package_name": "platform",
	      "columns": {"id": {"name": "id", "description": "The key.",
	                         "meta": {"owner": "platform", "pii": false},
	                         "tags": ["pk"], "data_type": "INTEGER"}},
	      "depends_on": {"nodes": ["seed.platform.raw"]}
	    },
	    "model.platform.alpha": {"name": "alpha", "resource_type": "model", "package_name": "platform"}
	  },
	  "sources": {
	    "source.platform.crm.raw_orders": {"name": "raw_orders", "source_name": "crm",
	                                        "identifier": "raw_orders"}
	  },
	  "disabled": {"model.platform.old": [{"name": "old"}]}
	}`

	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.json")
	if err := os.WriteFile(path, []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}

	m, err := LoadManifest(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	if m.Metadata.ProjectName != "platform" || m.Metadata.DbtVersion != "1.10.0" {
		t.Errorf("metadata = %+v", m.Metadata)
	}
	if len(m.Nodes) != 2 || len(m.Sources) != 1 {
		t.Fatalf("decoded %d nodes and %d sources, want 2 and 1", len(m.Nodes), len(m.Sources))
	}

	zeta := m.Nodes["model.platform.zeta"]
	if zeta.Order != 0 || m.Nodes["model.platform.alpha"].Order != 1 {
		t.Errorf("orders = %d and %d, want the order they appear in the file",
			zeta.Order, m.Nodes["model.platform.alpha"].Order)
	}
	if zeta.UniqueID != "model.platform.zeta" || zeta.Manifest != m {
		t.Error("the node was not linked back to its manifest")
	}
	if got := zeta.Column("id", false); got == nil || got.Description != "The key." ||
		got.DataType != "INTEGER" || !reflect.DeepEqual(got.Tags, []string{"pk"}) {
		t.Errorf("column = %+v", got)
	}
	if keys := zeta.Column("id", false).Meta.Keys(); !reflect.DeepEqual(keys, []string{"owner", "pii"}) {
		t.Errorf("meta keys = %v, want the file's order", keys)
	}
	if !reflect.DeepEqual(zeta.DependsOn.Nodes, []string{"seed.platform.raw"}) {
		t.Errorf("depends_on = %v", zeta.DependsOn.Nodes)
	}

	src := m.Sources["source.platform.crm.raw_orders"]
	if !src.IsSource() || src.Relation() != "raw_orders" {
		t.Errorf("source = %+v, want it typed as a source", src)
	}
}

func TestLoadManifestRejectsGarbage(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.json")
	if err := os.WriteFile(path, []byte(`["not", "an", "object"]`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadManifest(path); err == nil {
		t.Fatal("expected an error for a manifest that is not an object")
	} else if !strings.Contains(err.Error(), "manifest") {
		t.Errorf("error = %v, want it to name the file it failed on", err)
	}
}
