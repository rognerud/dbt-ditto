package runner_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/rognerud/dbt-ditto/internal/config"
	"github.com/rognerud/dbt-ditto/internal/dbt"
	"github.com/rognerud/dbt-ditto/internal/runner"
)

// The fixture is deliberately small, so the benchmarks run against a generated
// project instead: a wide staging layer over a documented seed, a chain of
// intermediate models to give inheritance some depth, and a mart layer that
// fans back out. Sizes are chosen to bracket what real warehouses look like.
var benchShapes = []struct {
	name    string
	models  int
	columns int
	depth   int
}{
	{"100models", 100, 20, 4},
	{"1000models", 1000, 20, 4},
	{"5000models_wide", 5000, 60, 6},
}

func BenchmarkRun(b *testing.B) {
	for _, shape := range benchShapes {
		b.Run(shape.name, func(b *testing.B) {
			src := generateProject(b, shape.models, shape.columns, shape.depth)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				b.StopTimer()
				// Every iteration starts from a project that has not been
				// documented yet, otherwise all but the first would measure the
				// much cheaper no-op path.
				work := b.TempDir()
				if err := copyTree(src, work); err != nil {
					b.Fatal(err)
				}
				cfg := &config.Config{
					Dir:      work,
					Projects: []config.ProjectRef{{Name: "bench", Path: work}},
				}
				b.StartTimer()

				if _, err := runner.Run(cfg, runner.Options{Organize: true}); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkRunNoOp measures the path a CI `--check` takes on an already
// documented project, which is how the tool is run most often.
func BenchmarkRunNoOp(b *testing.B) {
	for _, shape := range benchShapes {
		b.Run(shape.name, func(b *testing.B) {
			work := generateProject(b, shape.models, shape.columns, shape.depth)
			cfg := &config.Config{
				Dir:      work,
				Projects: []config.ProjectRef{{Name: "bench", Path: work}},
			}
			if _, err := runner.Run(cfg, runner.Options{Organize: true}); err != nil {
				b.Fatal(err)
			}

			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				rep, err := runner.Run(cfg, runner.Options{Organize: true, DryRun: true, Check: true})
				if err != nil {
					b.Fatal(err)
				}
				if len(rep.FilesWritten) != 0 {
					b.Fatalf("expected a no-op, got %d pending writes", len(rep.FilesWritten))
				}
			}
		})
	}
}

// generateProject writes a synthetic dbt project: dbt_project.yml plus the
// manifest and catalog that `dbt parse` and `dbt docs generate` would produce.
// It returns the project root.
func generateProject(tb testing.TB, models, columns, depth int) string {
	tb.Helper()
	root := tb.TempDir()

	if err := os.MkdirAll(filepath.Join(root, "models"), 0o755); err != nil {
		tb.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "target"), 0o755); err != nil {
		tb.Fatal(err)
	}
	project := "name: bench\nversion: \"1.0.0\"\nconfig-version: 2\nprofile: bench\n" +
		"model-paths: [\"models\"]\n\nmodels:\n  bench:\n    +dbt-osmosis: \"_{model}.yml\"\n"
	if err := os.WriteFile(filepath.Join(root, "dbt_project.yml"), []byte(project), 0o644); err != nil {
		tb.Fatal(err)
	}

	colNames := make([]string, columns)
	for i := range colNames {
		colNames[i] = fmt.Sprintf("column_%02d", i)
	}

	type jsonColumn struct {
		Name        string         `json:"name"`
		Description string         `json:"description,omitempty"`
		Meta        map[string]any `json:"meta,omitempty"`
		Tags        []string       `json:"tags,omitempty"`
		DataType    string         `json:"data_type,omitempty"`
		Config      map[string]any `json:"config,omitempty"`
	}
	type jsonNode struct {
		Name             string                `json:"name"`
		ResourceType     string                `json:"resource_type"`
		PackageName      string                `json:"package_name"`
		Schema           string                `json:"schema"`
		Database         string                `json:"database"`
		OriginalFilePath string                `json:"original_file_path"`
		Path             string                `json:"path"`
		FQN              []string              `json:"fqn"`
		Columns          map[string]jsonColumn `json:"columns"`
		DependsOn        struct {
			Nodes []string `json:"nodes"`
		} `json:"depends_on"`
		Config map[string]any `json:"config"`
	}

	nodes := map[string]jsonNode{}
	catalogNodes := map[string]map[string]any{}

	// The documented root every model ultimately inherits from.
	rootID := "seed.bench.raw_source"
	rootCols := map[string]jsonColumn{}
	for i, name := range colNames {
		rootCols[name] = jsonColumn{
			Name:        name,
			Description: fmt.Sprintf("Documentation for %s, written once at the root.", name),
			Meta:        map[string]any{"owner": "platform-team", "pii": i%3 == 0},
			Tags:        []string{"core"},
		}
	}
	nodes[rootID] = jsonNode{
		Name: "raw_source", ResourceType: "seed", PackageName: "bench",
		Schema: "main", Database: "bench",
		OriginalFilePath: "seeds/raw_source.csv", Path: "raw_source.csv",
		FQN: []string{"bench", "raw_source"}, Columns: rootCols,
	}

	// A chain of `depth` models, then the remaining models fan out from the end
	// of the chain, so most nodes have several generations above them.
	previous := rootID
	chain := make([]string, 0, depth)
	for d := 0; d < depth && d < models; d++ {
		name := fmt.Sprintf("int_layer_%d", d)
		id := "model.bench." + name
		n := jsonNode{
			Name: name, ResourceType: "model", PackageName: "bench",
			Schema: "main", Database: "bench",
			OriginalFilePath: "models/" + name + ".sql", Path: name + ".sql",
			FQN: []string{"bench", name}, Columns: map[string]jsonColumn{},
		}
		n.DependsOn.Nodes = []string{previous}
		nodes[id] = n
		chain = append(chain, id)
		previous = id
	}

	for i := len(chain); i < models; i++ {
		name := fmt.Sprintf("mart_%05d", i)
		id := "model.bench." + name
		n := jsonNode{
			Name: name, ResourceType: "model", PackageName: "bench",
			Schema: "main", Database: "bench",
			OriginalFilePath: "models/" + name + ".sql", Path: name + ".sql",
			FQN: []string{"bench", name}, Columns: map[string]jsonColumn{},
		}
		n.DependsOn.Nodes = []string{previous}
		// Give a share of the models a second parent so the graph has real
		// diamonds rather than being one long chain.
		if i%4 == 0 && len(chain) > 1 {
			n.DependsOn.Nodes = append(n.DependsOn.Nodes, chain[i%len(chain)])
		}
		nodes[id] = n
	}

	// The catalog knows every column of every relation; nothing but the root is
	// documented, so every model has something to inherit.
	for id, n := range nodes {
		cols := map[string]any{}
		for i, name := range colNames {
			cols[name] = map[string]any{"name": name, "index": i, "type": "VARCHAR"}
		}
		catalogNodes[id] = map[string]any{
			"metadata": map[string]any{"name": n.Name, "schema": n.Schema, "database": n.Database},
			"columns":  cols,
		}
	}

	manifest := map[string]any{
		"metadata": map[string]any{"project_name": "bench", "dbt_version": "1.10.0", "adapter_type": "duckdb"},
		"nodes":    nodes,
		"sources":  map[string]any{},
		// Ballast: a real manifest is mostly things dbt-ditto skips, and the
		// benchmark should pay the cost of skipping them.
		"macros":    ballast(models),
		"child_map": ballast(models),
		"docs":      ballast(models),
	}

	writeJSON(tb, filepath.Join(root, "target", "manifest.json"), manifest)
	writeJSON(tb, filepath.Join(root, "target", "catalog.json"), map[string]any{
		"nodes": catalogNodes, "sources": map[string]any{},
	})
	return root
}

// ballast produces a chunk of manifest the tool has no use for.
func ballast(n int) map[string]any {
	out := make(map[string]any, n)
	for i := 0; i < n; i++ {
		out[fmt.Sprintf("entry.%d", i)] = map[string]any{
			"name":    fmt.Sprintf("entry_%d", i),
			"sql":     "select 1 as one, 2 as two, 3 as three from somewhere where x = 1",
			"depends": []string{"a", "b", "c"},
			"nested":  map[string]any{"deeper": map[string]any{"deepest": []int{1, 2, 3}}},
		}
	}
	return out
}

func writeJSON(tb testing.TB, path string, v any) {
	tb.Helper()
	f, err := os.Create(path)
	if err != nil {
		tb.Fatal(err)
	}
	defer f.Close()
	if err := json.NewEncoder(f).Encode(v); err != nil {
		tb.Fatal(err)
	}
}

// BenchmarkLoadManifest isolates artifact reading, which dominates a run on a
// project of any size.
func BenchmarkLoadManifest(b *testing.B) {
	for _, shape := range benchShapes {
		b.Run(shape.name, func(b *testing.B) {
			root := generateProject(b, shape.models, shape.columns, shape.depth)
			path := filepath.Join(root, "target", "manifest.json")
			info, err := os.Stat(path)
			if err != nil {
				b.Fatal(err)
			}
			b.SetBytes(info.Size())
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := dbt.LoadManifest(path); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
