package runner_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/rognerud/dbt-ditto/internal/config"
	"github.com/rognerud/dbt-ditto/internal/runner"
)

// The version matrix.

// matrixCase is one captured dbt version.
type matrixCase struct {
	dir  string
	Meta struct {
		Adapter          string `json:"adapter"`
		AdapterVersion   string `json:"adapter_version"`
		DbtVersion       string `json:"dbt_version"`
		DbtSchemaVersion string `json:"dbt_schema_version"`
		// EmulatedBy names the stand-in that produced these artifacts, if any:
		// Snowflake's come from fakesnow, a real dbt-snowflake over DuckDB.
		EmulatedBy string `json:"emulated_by"`
	}
}

// projectDir is the source project the artifacts were built from.
func (c matrixCase) projectDir(t *testing.T) string {
	t.Helper()
	switch c.Meta.Adapter {
	case "snowflake", "postgres", "bigquery":
		return repoPath(t, "testdata", "matrix", c.Meta.Adapter)
	default:
		return repoPath(t, "testdata", "matrix", "project")
	}
}

func (c matrixCase) name() string { return c.Meta.Adapter + "-" + c.Meta.DbtVersion }

// loadMatrix reads every captured version.
func loadMatrix(t *testing.T) []matrixCase {
	t.Helper()
	// The matrix replays every captured adapter and version.
	if testing.Short() {
		t.Skip("skipping the version matrix in short mode")
	}
	root := repoPath(t, "testdata", "matrix", "artifacts")
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("read matrix artifacts: %v (run ./scripts/matrix.sh)", err)
	}

	var cases []matrixCase
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		c := matrixCase{dir: filepath.Join(root, e.Name())}
		raw, err := os.ReadFile(filepath.Join(c.dir, "meta.json"))
		if err != nil {
			t.Errorf("%s: %v", e.Name(), err)
			continue
		}
		if err := json.Unmarshal(raw, &c.Meta); err != nil {
			t.Errorf("%s: %v", e.Name(), err)
			continue
		}
		cases = append(cases, c)
	}
	if len(cases) == 0 {
		t.Fatal("no captured dbt versions; run ./scripts/matrix.sh")
	}
	sort.Slice(cases, func(i, j int) bool { return cases[i].dir < cases[j].dir })
	return cases
}

// matrixProject stages the matrix project with one version's artifacts as its
// target directory, and returns the project root.
func matrixProject(t *testing.T, c matrixCase) string {
	t.Helper()
	root := t.TempDir()
	if err := copyTree(c.projectDir(t), root); err != nil {
		t.Fatalf("copy project: %v", err)
	}
	target := filepath.Join(root, "target")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	// The artifacts are committed gzipped; both readers understand `.gz`.
	for _, name := range []string{"manifest.json.gz", "catalog.json.gz"} {
		data, err := os.ReadFile(filepath.Join(c.dir, name))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if err := os.WriteFile(filepath.Join(target, name), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func matrixConfig(root string) *config.Config {
	return &config.Config{
		Dir:      root,
		Projects: []config.ProjectRef{{Name: "matrix", Path: root}},
	}
}

// TestMatrixRuns is the blunt instrument: every dbt version must parse, resolve
// and write without error, and produce what the fixture is built to produce.
func TestMatrixRuns(t *testing.T) {
	for _, c := range loadMatrix(t) {
		t.Run(c.name(), func(t *testing.T) {
			root := matrixProject(t, c)
			rep, err := runner.Run(matrixConfig(root), runner.Options{Organize: true})
			if err != nil {
				t.Fatalf("run: %v", err)
			}
			if rep.NodesScanned == 0 {
				t.Fatal("no nodes were scanned: the manifest did not decode")
			}
			if len(rep.FilesWritten) == 0 {
				t.Fatal("nothing was written")
			}

			files := snapshotTree(t, root)
			stg := files["models/_stg_orders.yml"]
			if stg == "" {
				t.Fatal("stg_orders was not documented")
			}
			// Inheritance from the seed has to work on every version.
			for _, want := range []string{
				"Surrogate key for an order.",
				"Surrogate key for a customer.",
				"Order gross value in minor currency units.",
			} {
				if !strings.Contains(stg, want) {
					t.Errorf("stg_orders did not inherit %q:\n%s", want, stg)
				}
			}
		})
	}
}

// The manifest's version decides where column meta goes: dbt gained column-level
// `config:` in 1.9.6, and writing meta there on an older dbt loses it silently.
func TestMatrixConfigBlockFollowsTheDbtVersion(t *testing.T) {
	for _, c := range loadMatrix(t) {
		t.Run(c.name(), func(t *testing.T) {
			root := matrixProject(t, c)
			if _, err := runner.Run(matrixConfig(root), runner.Options{Organize: true}); err != nil {
				t.Fatalf("run: %v", err)
			}

			stg := snapshotTree(t, root)["models/_stg_orders.yml"]
			if !strings.Contains(stg, "owner: commerce-team") {
				t.Fatalf("meta was not inherited at all:\n%s", stg)
			}

			nested := strings.Contains(stg, "config:")
			want := atLeast(c.Meta.DbtVersion, 1, 9, 6)
			if nested != want {
				t.Errorf("dbt %s: meta nested under config = %v, want %v:\n%s",
					c.Meta.DbtVersion, nested, want, stg)
			}
		})
	}
}

// A rerun must change nothing, on every version — the assertion most likely to
// catch a version-specific quirk.
func TestMatrixRunsAreIdempotent(t *testing.T) {
	for _, c := range loadMatrix(t) {
		t.Run(c.name(), func(t *testing.T) {
			root := matrixProject(t, c)
			cfg := matrixConfig(root)

			if _, err := runner.Run(cfg, runner.Options{Organize: true}); err != nil {
				t.Fatalf("first run: %v", err)
			}
			second, err := runner.Run(cfg, runner.Options{Organize: true})
			if err != nil {
				t.Fatalf("second run: %v", err)
			}
			if len(second.FilesWritten) != 0 || len(second.FilesDeleted) != 0 {
				t.Errorf("second run was not a no-op: wrote %v, deleted %v",
					second.FilesWritten, second.FilesDeleted)
			}
		})
	}
}

// Every version must agree on the column set and the documentation.
func TestMatrixVersionsAgree(t *testing.T) {
	cases := loadMatrix(t)
	if len(cases) < 2 {
		t.Skip("need at least two captured versions to compare")
	}

	type result struct {
		adapter string
		version string
		columns []string
		descs   map[string]string
	}
	var results []result

	for _, c := range cases {
		root := matrixProject(t, c)
		if _, err := runner.Run(matrixConfig(root), runner.Options{Organize: true}); err != nil {
			t.Fatalf("%s: run: %v", c.name(), err)
		}
		doc := snapshotTree(t, root)["models/_stg_orders.yml"]
		r := result{adapter: c.Meta.Adapter, version: c.Meta.DbtVersion, descs: map[string]string{}}
		for _, line := range strings.Split(doc, "\n") {
			line = strings.TrimSpace(line)
			switch {
			case strings.HasPrefix(line, "- name: "):
				r.columns = append(r.columns, strings.TrimPrefix(line, "- name: "))
			case strings.HasPrefix(line, "description: ") && len(r.columns) > 0:
				r.descs[r.columns[len(r.columns)-1]] = strings.TrimPrefix(line, "description: ")
			}
		}
		results = append(results, r)
	}

	// Within an adapter, not across: Snowflake upper-cases its identifiers.
	byAdapter := map[string][]result{}
	for _, r := range results {
		byAdapter[r.adapter] = append(byAdapter[r.adapter], r)
	}
	for adapter, group := range byAdapter {
		if len(group) < 2 {
			continue
		}
		first := group[0]
		for _, r := range group[1:] {
			if strings.Join(r.columns, ",") != strings.Join(first.columns, ",") {
				t.Errorf("%s: dbt %s produced columns %v, but dbt %s produced %v",
					adapter, r.version, r.columns, first.version, first.columns)
			}
			for column, desc := range first.descs {
				if got := r.descs[column]; got != desc {
					t.Errorf("%s: dbt %s describes %s as %q, but dbt %s says %q",
						adapter, r.version, column, got, first.version, desc)
				}
			}
		}
	}
}

// Snowflake upper-cases every unquoted identifier, so its catalog reports
// ORDER_ID where the YAML says order_id: inheritance has to bridge that, a new
// column takes the warehouse's spelling, and an existing entry keeps its own.
func TestMatrixSnowflakeCaseHandling(t *testing.T) {
	var found bool
	for _, c := range loadMatrix(t) {
		if c.Meta.Adapter != "snowflake" {
			continue
		}
		found = true
		t.Run(c.name(), func(t *testing.T) {
			root := matrixProject(t, c)
			if _, err := runner.Run(matrixConfig(root), runner.Options{Organize: true}); err != nil {
				t.Fatalf("run: %v", err)
			}
			files := snapshotTree(t, root)

			// A model with no YAML: every column is new, so each takes the warehouse's.
			stg := files["models/_stg_orders.yml"]
			for _, column := range []string{"ORDER_ID", "CUSTOMER_ID", "STATUS", "AMOUNT_CENTS"} {
				if !strings.Contains(stg, "- name: "+column+"\n") {
					t.Errorf("want the warehouse spelling %s:\n%s", column, stg)
				}
			}
			// ...and it still inherited across the case difference.
			if !strings.Contains(stg, "Surrogate key for an order.") {
				t.Errorf("ORDER_ID did not inherit from order_id:\n%s", stg)
			}
			// Snowflake's own type names, not DuckDB's.
			if !strings.Contains(stg, "data_type: NUMBER") || !strings.Contains(stg, "data_type: TEXT") {
				t.Errorf("want Snowflake types:\n%s", stg)
			}

			// The seed is documented in lower case already, and must stay that way.
			seeds := files["seeds/_seeds.yml"]
			if !strings.Contains(seeds, "- name: order_id\n") {
				t.Errorf("an existing lower-case entry was rewritten:\n%s", seeds)
			}
			if strings.Contains(seeds, "- name: ORDER_ID\n") {
				t.Errorf("the seed gained a duplicate upper-case entry:\n%s", seeds)
			}
		})
	}
	if !found {
		t.Skip("no Snowflake artifacts captured; run ./scripts/matrix.sh")
	}
}

// The matrix is only meaningful if it actually spans the boundary it claims to.
func TestMatrixSpansTheConfigBlockBoundary(t *testing.T) {
	var below, above []string
	for _, c := range loadMatrix(t) {
		if atLeast(c.Meta.DbtVersion, 1, 9, 6) {
			above = append(above, c.Meta.DbtVersion)
		} else {
			below = append(below, c.Meta.DbtVersion)
		}
	}
	if len(below) == 0 || len(above) == 0 {
		t.Errorf("the matrix must straddle dbt 1.9.6 to be worth running; "+
			"below: %v, at or above: %v", below, above)
	}
}

// Postgres is dbt's reference adapter and the matrix's control.
func TestMatrixPostgres(t *testing.T) {
	var found bool
	for _, c := range loadMatrix(t) {
		if c.Meta.Adapter != "postgres" {
			continue
		}
		found = true
		t.Run(c.name(), func(t *testing.T) {
			root := matrixProject(t, c)
			if _, err := runner.Run(matrixConfig(root), runner.Options{Organize: true}); err != nil {
				t.Fatalf("run: %v", err)
			}
			files := snapshotTree(t, root)

			// Postgres keeps unquoted identifiers lower case, and has its own types.
			stg := files["models/_stg_orders.yml"]
			if !strings.Contains(stg, "- name: order_id\n") {
				t.Errorf("want lower-case identifiers:\n%s", stg)
			}
			if !strings.Contains(stg, "data_type: integer") || !strings.Contains(stg, "data_type: text") {
				t.Errorf("want Postgres types:\n%s", stg)
			}
			if !strings.Contains(stg, "Surrogate key for an order.") {
				t.Errorf("inheritance from the seed failed:\n%s", stg)
			}

			// `channel` carries a COMMENT ON COLUMN and is documented nowhere in dbt.
			seeds := files["seeds/_seeds.yml"]
			if !strings.Contains(seeds, "Sales channel the order arrived through.") {
				t.Errorf("the Postgres column comment did not reach the YAML:\n%s", seeds)
			}
			// ...while a column dbt documents keeps the hand-written text.
			if !strings.Contains(seeds, "Order lifecycle state: one of completed, pending or returned.") {
				t.Errorf("a hand-written description was lost:\n%s", seeds)
			}
		})
	}
	if !found {
		t.Skip("no Postgres artifacts captured; run ./scripts/matrix.sh with Docker running")
	}
}

// BigQuery, from a manifest dbt-bigquery itself wrote.
func TestMatrixBigQuery(t *testing.T) {
	var found bool
	for _, c := range loadMatrix(t) {
		if c.Meta.Adapter != "bigquery" {
			continue
		}
		found = true
		t.Run(c.name(), func(t *testing.T) {
			root := matrixProject(t, c)
			if _, err := runner.Run(matrixConfig(root), runner.Options{Organize: true}); err != nil {
				t.Fatalf("run: %v", err)
			}

			stg := snapshotTree(t, root)["models/_stg_orders.yml"]
			if stg == "" {
				t.Fatal("stg_orders was not documented")
			}
			// BigQuery keeps unquoted identifiers as written, and has its own type names.
			if !strings.Contains(stg, "- name: order_id\n") {
				t.Errorf("want the identifiers as written:\n%s", stg)
			}
			if !strings.Contains(stg, "data_type: INT64") || !strings.Contains(stg, "data_type: STRING") {
				t.Errorf("want BigQuery types:\n%s", stg)
			}
			if !strings.Contains(stg, "Surrogate key for an order.") {
				t.Errorf("inheritance from the root model failed:\n%s", stg)
			}
		})
	}
	if !found {
		t.Skip("no BigQuery artifacts captured; run ./scripts/matrix.sh")
	}
}

// Every adapter the rig claims to cover must actually be present.
func TestMatrixCoversEveryAdapter(t *testing.T) {
	seen := map[string]bool{}
	for _, c := range loadMatrix(t) {
		seen[c.Meta.Adapter] = true
	}
	for _, adapter := range []string{"duckdb", "postgres", "snowflake", "bigquery"} {
		if !seen[adapter] {
			t.Errorf("no %s artifacts captured; run ./scripts/matrix.sh", adapter)
		}
	}
}

// atLeast compares a dotted version against major.minor.patch.
func atLeast(version string, major, minor, patch int) bool {
	parts := strings.SplitN(version, ".", 4)
	got := []int{0, 0, 0}
	for i := 0; i < 3 && i < len(parts); i++ {
		n := 0
		for _, ch := range parts[i] {
			if ch < '0' || ch > '9' {
				break
			}
			n = n*10 + int(ch-'0')
		}
		got[i] = n
	}
	want := []int{major, minor, patch}
	for i := 0; i < 3; i++ {
		if got[i] != want[i] {
			return got[i] > want[i]
		}
	}
	return true
}
