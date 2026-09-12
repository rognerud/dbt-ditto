package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeLoomProject lays out a project directory with a dbt_loom.config.yml and
// the upstream manifest it points at.
func writeLoomProject(t *testing.T, loom string) (dir, root string) {
	t.Helper()
	dir = t.TempDir()
	root = filepath.Join(dir, "analytics")
	upstream := filepath.Join(dir, "platform", "target")
	for _, d := range []string{root, upstream} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(upstream, "manifest.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, loomFilename), []byte(loom), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "dbt_ditto.yml"),
		[]byte("projects:\n  - name: analytics\n    path: analytics\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir, root
}

// A project that already tells dbt-loom where its upstreams live should not have
// to repeat that list in dbt_ditto.yml.
func TestLoadPicksUpLoomFileManifests(t *testing.T) {
	dir, _ := writeLoomProject(t, `manifests:
  - name: platform
    type: file
    config:
      path: ../platform/target/manifest.json
`)

	c, err := Load(filepath.Join(dir, "dbt_ditto.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Projects) != 2 {
		t.Fatalf("got %d projects, want 2: %+v", len(c.Projects), c.Projects)
	}
	up := c.Projects[1]
	if up.Name != "platform" || !up.Upstream {
		t.Fatalf("upstream ref = %+v, want name platform and upstream true", up)
	}
	want := filepath.Join(dir, "platform", "target", "manifest.json")
	if up.Manifest != want {
		t.Fatalf("manifest = %q, want %q", up.Manifest, want)
	}
	if len(c.Notes) != 0 {
		t.Fatalf("unexpected notes: %v", c.Notes)
	}
}

// The remote loom backends are dbt-loom fetching artifacts over the network.
// dbt-ditto does not do that, and saying nothing would leave the analyst
// wondering why half their documentation did not arrive.
func TestLoadNotesUnreachableLoomManifests(t *testing.T) {
	dir, _ := writeLoomProject(t, `manifests:
  - name: platform
    type: dbt_cloud
    config:
      account_id: 1
  - name: missing
    type: file
    config:
      path: ../nowhere/manifest.json
`)

	c, err := Load(filepath.Join(dir, "dbt_ditto.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Projects) != 1 {
		t.Fatalf("got %d projects, want 1: %+v", len(c.Projects), c.Projects)
	}
	if len(c.Notes) != 2 {
		t.Fatalf("got %d notes, want 2: %v", len(c.Notes), c.Notes)
	}
	if !strings.Contains(c.Notes[0], "dbt_cloud") {
		t.Errorf("note %q does not name the unsupported type", c.Notes[0])
	}
	if !strings.Contains(c.Notes[1], "missing") {
		t.Errorf("note %q does not name the missing manifest", c.Notes[1])
	}
}

// An explicit entry in dbt_ditto.yml is the analyst overriding loom, most
// likely to point at a checkout they can actually write to.
func TestLoomEntryIsStillAddedForAProjectAlreadyListed(t *testing.T) {
	dir, _ := writeLoomProject(t, `manifests:
  - name: platform
    type: file
    config:
      path: ../platform/target/manifest.json
`)
	if err := os.WriteFile(filepath.Join(dir, "dbt_ditto.yml"),
		[]byte("projects:\n  - path: analytics\n  - path: platform\n"),
		0o644); err != nil {
		t.Fatal(err)
	}

	c, err := Load(filepath.Join(dir, "dbt_ditto.yml"))
	if err != nil {
		t.Fatal(err)
	}
	// Three entries, because at this stage nothing has read a manifest: a path
	// and a loom name cannot be known to be the same project yet. The duplicate
	// is dropped after loading, by the name each manifest reports.
	if len(c.Projects) != 3 {
		t.Fatalf("got %d projects, want 3: %+v", len(c.Projects), c.Projects)
	}
	if c.Projects[2].Manifest == "" || !c.Projects[2].Upstream {
		t.Errorf("loom entry = %+v, want an upstream manifest ref", c.Projects[2])
	}
}

// Loom naming the same project twice is a duplicate this stage can see, since
// both names come from the same file.
func TestLoomEntriesAreDedupedAmongThemselves(t *testing.T) {
	dir, _ := writeLoomProject(t, `manifests:
  - name: platform
    type: file
    config:
      path: ../platform/target/manifest.json
  - name: platform
    type: file
    config:
      path: ../platform/target/manifest.json
`)

	c, err := Load(filepath.Join(dir, "dbt_ditto.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Projects) != 2 {
		t.Fatalf("got %d projects, want 2: %+v", len(c.Projects), c.Projects)
	}
}

func TestLoomDiscoveryCanBeTurnedOff(t *testing.T) {
	dir, _ := writeLoomProject(t, `manifests:
  - name: platform
    type: file
    config:
      path: ../platform/target/manifest.json
`)
	if err := os.WriteFile(filepath.Join(dir, "dbt_ditto.yml"),
		[]byte("loom: false\nprojects:\n  - name: analytics\n    path: analytics\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	c, err := Load(filepath.Join(dir, "dbt_ditto.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Projects) != 1 {
		t.Fatalf("got %d projects, want 1: %+v", len(c.Projects), c.Projects)
	}
}

// dbt-loom reads DBT_LOOM_CONFIG in preference to the conventional filename.
func TestLoomConfigEnvOverride(t *testing.T) {
	dir, root := writeLoomProject(t, "manifests: []\n")
	alt := filepath.Join(root, "loom-ci.yml")
	if err := os.WriteFile(alt, []byte(`manifests:
  - name: platform
    type: file
    config:
      path: ../platform/target/manifest.json
`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv(loomEnv, "loom-ci.yml")

	c, err := Load(filepath.Join(dir, "dbt_ditto.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Projects) != 2 {
		t.Fatalf("got %d projects, want 2: %+v", len(c.Projects), c.Projects)
	}
}
