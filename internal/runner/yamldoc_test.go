package runner

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/rognerud/dbt-ditto/internal/config"
	"github.com/rognerud/dbt-ditto/internal/dbt"
	"github.com/rognerud/dbt-ditto/internal/inherit"
	"github.com/rognerud/dbt-ditto/internal/yamlfile"
)

func newFile(t *testing.T) *yamlfile.File {
	t.Helper()
	return yamlfile.New(filepath.Join(t.TempDir(), "schema.yml"))
}

func render(t *testing.T, f *yamlfile.File) string {
	t.Helper()
	out, err := f.Render()
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	return string(out)
}

func writeOptsFor(configBlock bool, tweak func(*config.Config)) writeOpts {
	c := &config.Config{}
	if tweak != nil {
		tweak(c)
	}
	return writeOpts{cfg: c.Resolve(), configBlock: configBlock}
}

func model(name string) *dbt.Node {
	return &dbt.Node{UniqueID: "model.p." + name, Name: name, ResourceType: "model"}
}

// dbt >= 1.9.6 wants column meta and tags nested under `config:`; older dbt
// wants them at the top level. Writing the wrong shape silently loses the data,
// so both are covered.
func TestColumnMetaPlacementFollowsTheConfigBlockSetting(t *testing.T) {
	doc := &inherit.NodeDoc{Columns: []inherit.ColumnDoc{{
		Name: "id", Description: "The key.", SetDescription: true,
		Meta: []inherit.MetaEntry{{Key: "owner", Value: "platform"}},
		Tags: []string{"pk"},
	}}}

	nested := newFile(t)
	writeDoc(nested, model("m"), doc, writeOptsFor(true, nil))
	got := render(t, nested)
	if !strings.Contains(got, "config:") || !strings.Contains(got, "meta:") {
		t.Errorf("want meta nested under config:\n%s", got)
	}
	if strings.Index(got, "config:") > strings.Index(got, "owner: platform") {
		t.Errorf("meta is not inside the config block:\n%s", got)
	}

	flat := newFile(t)
	writeDoc(flat, model("m"), doc, writeOptsFor(false, nil))
	got = render(t, flat)
	if strings.Contains(got, "config:") {
		t.Errorf("want meta at the top level, got a config block:\n%s", got)
	}
	if !strings.Contains(got, "owner: platform") || !strings.Contains(got, "- pk") {
		t.Errorf("meta or tags missing:\n%s", got)
	}
}

// A column that already carries top-level meta must end up with it in one place
// only when the shape changes, never in both.
func TestMetaIsNotDuplicatedWhenTheShapeChanges(t *testing.T) {
	f := newFile(t)

	// Seed the file with the classic shape.
	writeDoc(f, model("m"), &inherit.NodeDoc{Columns: []inherit.ColumnDoc{{
		Name: "id",
		Meta: []inherit.MetaEntry{{Key: "owner", Value: "platform"}},
	}}}, writeOptsFor(false, nil))
	if before := render(t, f); !strings.Contains(before, "owner: platform") {
		t.Fatalf("setup failed:\n%s", before)
	}

	// Now write the same column in the nested shape.
	writeDoc(f, model("m"), &inherit.NodeDoc{Columns: []inherit.ColumnDoc{{
		Name: "id", Existing: true,
		Meta: []inherit.MetaEntry{{Key: "owner", Value: "platform"}},
	}}}, writeOptsFor(true, nil))

	got := render(t, f)
	if strings.Count(got, "owner: platform") != 1 {
		t.Errorf("meta appears %d times, want exactly one:\n%s",
			strings.Count(got, "owner: platform"), got)
	}
	if !strings.Contains(got, "config:") {
		t.Errorf("want the nested shape:\n%s", got)
	}
}

// Meta is written in the resolved order, not in map order.
func TestMetaKeyOrderIsHonoured(t *testing.T) {
	f := newFile(t)
	writeDoc(f, model("m"), &inherit.NodeDoc{Columns: []inherit.ColumnDoc{{
		Name: "id",
		Meta: []inherit.MetaEntry{
			{Key: "zulu", Value: 1},
			{Key: "alpha", Value: true},
			{Key: "mike", Value: "x"},
		},
	}}}, writeOptsFor(false, nil))

	got := render(t, f)
	zulu, alpha, mike := strings.Index(got, "zulu"), strings.Index(got, "alpha"), strings.Index(got, "mike")
	if !(zulu < alpha && alpha < mike) {
		t.Errorf("keys are not in the resolved order:\n%s", got)
	}
	if !strings.Contains(got, "zulu: 1") || !strings.Contains(got, "alpha: true") {
		t.Errorf("values were not written as their own types:\n%s", got)
	}
}

// An empty description is noise, and dbt-osmosis does not write one.
func TestEmptyDescriptionsAreNotWritten(t *testing.T) {
	f := newFile(t)
	writeDoc(f, model("m"), &inherit.NodeDoc{Columns: []inherit.ColumnDoc{
		{Name: "documented", Description: "Something.", SetDescription: true},
		{Name: "bare", DataType: "VARCHAR", SetDataType: true},
	}}, writeOptsFor(false, nil))

	got := render(t, f)
	if strings.Contains(got, `description: ""`) || strings.Contains(got, "description: |-\n") {
		t.Errorf("an empty description was written:\n%s", got)
	}
	if strings.Count(got, "description:") != 1 {
		t.Errorf("want one description, got %d:\n%s", strings.Count(got, "description:"), got)
	}
	if !strings.Contains(got, "data_type: VARCHAR") {
		t.Errorf("data type missing:\n%s", got)
	}
}

// Columns the resolver dropped go, columns it never mentioned stay.
func TestDroppedColumnsGoAndUnmentionedOnesStay(t *testing.T) {
	f := newFile(t)
	writeDoc(f, model("m"), &inherit.NodeDoc{Columns: []inherit.ColumnDoc{
		{Name: "keep"}, {Name: "stale"}, {Name: "untouched"},
	}}, writeOptsFor(false, nil))

	writeDoc(f, model("m"), &inherit.NodeDoc{
		Columns: []inherit.ColumnDoc{{Name: "keep", Existing: true}},
		Drop:    []string{"stale"},
	}, writeOptsFor(false, nil))

	got := render(t, f)
	if strings.Contains(got, "stale") {
		t.Errorf("the dropped column is still there:\n%s", got)
	}
	if !strings.Contains(got, "untouched") {
		t.Errorf("a column the resolver said nothing about was removed:\n%s", got)
	}
}

// The `osmosis` comment mode reproduces dbt-osmosis' loss of comments inside a
// column list; the default keeps them.
func TestCommentHandlingModes(t *testing.T) {
	build := func(comments string) string {
		f := newFile(t)
		writeDoc(f, model("m"), &inherit.NodeDoc{Columns: []inherit.ColumnDoc{
			{Name: "first"}, {Name: "second"},
		}}, writeOptsFor(false, nil))

		// Attach a comment to each column, as a hand-written file would have.
		entry := f.Entry("models", "m", false)
		cols := yamlfile.MapGet(entry, "columns")
		cols.Content[0].HeadComment = "# about the first"
		cols.Content[1].HeadComment = "# about the second"

		mode := comments
		writeDoc(f, model("m"), &inherit.NodeDoc{Columns: []inherit.ColumnDoc{
			{Name: "second", Existing: true}, {Name: "first", Existing: true},
		}}, writeOptsFor(false, func(c *config.Config) { c.Output.Comments = &mode }))
		return render(t, f)
	}

	follow := build(config.CommentsFollow)
	if !strings.Contains(follow, "# about the first") || !strings.Contains(follow, "# about the second") {
		t.Errorf("the default should keep every comment:\n%s", follow)
	}

	osmosis := build(config.CommentsOsmosis)
	if strings.Contains(osmosis, "# about the second") {
		t.Errorf("osmosis mode should have dropped the second comment:\n%s", osmosis)
	}
	if !strings.Contains(osmosis, "# about the first") {
		t.Errorf("osmosis mode keeps the comment above the first entry:\n%s", osmosis)
	}
}

// Sources live under sources[].tables[], not under models[].
func TestSourcesAreWrittenInTheirOwnShape(t *testing.T) {
	src := &dbt.Node{
		UniqueID: "source.p.crm.raw_orders", Name: "raw_orders",
		ResourceType: "source", SourceName: "crm", Identifier: "raw_orders",
	}
	f := newFile(t)
	writeDoc(f, src, &inherit.NodeDoc{Columns: []inherit.ColumnDoc{
		{Name: "order_id", Description: "The key.", SetDescription: true},
	}}, writeOptsFor(false, nil))

	got := render(t, f)
	for _, want := range []string{"sources:", "name: crm", "tables:", "name: raw_orders", "The key."} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "models:") {
		t.Errorf("a source was written under models:\n%s", got)
	}
}

// Reading meta back has to understand both shapes, so a project that has been
// migrated between them is not seen as having lost its metadata.
func TestReadExistingUnderstandsBothMetaShapes(t *testing.T) {
	f := newFile(t)

	writeDoc(f, model("m"), &inherit.NodeDoc{Columns: []inherit.ColumnDoc{{
		Name: "flat",
		Meta: []inherit.MetaEntry{{Key: "owner", Value: "a"}},
		Tags: []string{"t1"},
	}}}, writeOptsFor(false, nil))
	writeDoc(f, model("m"), &inherit.NodeDoc{Columns: []inherit.ColumnDoc{
		{Name: "flat", Existing: true, Meta: []inherit.MetaEntry{{Key: "owner", Value: "a"}}, Tags: []string{"t1"}},
		{Name: "nested", Meta: []inherit.MetaEntry{{Key: "owner", Value: "b"}}, Tags: []string{"t2"}},
	}}, writeOptsFor(true, nil))

	got := readExisting(f, model("m"))
	if len(got.Columns) != 2 {
		t.Fatalf("read %d columns, want 2", len(got.Columns))
	}
	for _, c := range got.Columns {
		if len(c.Meta) != 1 || c.Meta[0].Key != "owner" {
			t.Errorf("%s: meta = %v, want owner read back", c.Name, c.Meta)
		}
		if len(c.Tags) != 1 {
			t.Errorf("%s: tags = %v, want one tag read back", c.Name, c.Tags)
		}
	}
}
