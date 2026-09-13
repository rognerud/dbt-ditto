package runner_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rognerud/dbt-ditto/internal/config"
	"github.com/rognerud/dbt-ditto/internal/runner"
)

// The BigQuery and Snowflake providers cannot be run from a test: one needs a
// Google project and the other a Snowflake account. What *can* be proved with
// neither is the part that breaks silently if either side drifts — that the
// request dbt-ditto writes is the request a provider reads, and that the
// response a provider writes is the response dbt-ditto applies.
//
// packaging/providers/echo.py answers from a file using the same shared module
// the real providers use, so running it end to end exercises the contract on
// both sides. The providers' own logic is tested offline in
// packaging/providers/test_providers.py.
func TestPythonProviderRoundTrip(t *testing.T) {
	python := findPython(t)
	repo := repoRoot(t)

	dir := newStage().write(t, t.TempDir())
	answers := filepath.Join(dir, "answers.json")
	writeFile(t, answers, mustJSON(t, []map[string]any{{
		"unique_id":   externalSource,
		"description": "Orders as the CRM records them.",
		"labels":      map[string]string{"owner": "crm-team"},
		"columns": []map[string]any{{
			"name":        "order_id",
			"data_type":   "BIGINT",
			"description": "Identifier the CRM assigned.",
			"index":       1,
			"labels":      map[string]string{"classification": "restricted"},
			"extra":       map[string]any{"policy_tags": []string{"taxonomies/1/policyTags/2"}},
		}},
	}}))

	cfg := stageConfig(dir)
	cfg.Inheritance.ExtraKeys = []string{"policy_tags"}
	cfg.Sources.Providers = []config.SourceProvider{{
		Command: strings.Join([]string{
			shellQuote(python),
			shellQuote(filepath.Join(repo, "packaging", "providers", "echo.py")),
			shellQuote(answers),
		}, " "),
	}}

	snap := runStageWith(t, dir, cfg, runner.Options{Organize: true, RefreshSources: true})

	sources := snap.file(t, "models/_sources.yml")
	contains(t, sources, "Orders as the CRM records them.", "the table's own description did not arrive")
	contains(t, sources, "Identifier the CRM assigned.", "the column description did not arrive")
	contains(t, sources, "classification: restricted", "the column's labels were not routed into meta")
	contains(t, sources, "policy_tags", "the extra key was not carried")

	// And it travelled: the model reading the source inherits all three.
	stgOrders := snap.file(t, "models/_stg_orders.yml")
	contains(t, stgOrders, "classification: restricted",
		"a column label should reach the model that reads the source")
	contains(t, stgOrders, "policy_tags",
		"a policy tag has no reason to change as the column moves downstream")

	// The refresh wrote a cache, and a second run reads it without spawning
	// anything — which is what makes --check work with no credentials.
	cached := runStage(t, dir, cfg)
	contains(t, cached.file(t, "models/_sources.yml"), "Orders as the CRM records them.",
		"the cache written by the refresh was not read back")
}

// The request has to carry enough for a provider to authenticate the way dbt
// does, or every provider ends up with its own copy of the credentials.
func TestProviderIsToldWhereDbtKeepsItsCredentials(t *testing.T) {
	dir := newStage().write(t, t.TempDir())
	captured := filepath.Join(dir, "request.json")

	cfg := stageConfig(dir)
	cfg.Sources.Providers = []config.SourceProvider{{
		Command: "cat > " + shellQuote(captured) + "; printf '%s' '{\"version\":1,\"sources\":[]}'",
	}}
	runStageWith(t, dir, cfg, runner.Options{Organize: true, RefreshSources: true, Target: "prod"})

	var req struct {
		Version  int `json:"version"`
		Projects []struct {
			Name        string `json:"name"`
			Root        string `json:"root"`
			Profile     string `json:"profile"`
			Target      string `json:"target"`
			ProfilesDir string `json:"profiles_dir"`
		} `json:"projects"`
		Sources []struct {
			UniqueID   string `json:"unique_id"`
			Database   string `json:"database"`
			Schema     string `json:"schema"`
			Identifier string `json:"identifier"`
			Project    string `json:"project"`
		} `json:"sources"`
	}
	body, err := os.ReadFile(captured)
	if err != nil {
		t.Fatalf("the provider was never run: %v", err)
	}
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatalf("the request was not valid JSON: %v", err)
	}

	if len(req.Projects) != 1 {
		t.Fatalf("expected one project in the request, got %+v", req.Projects)
	}
	p := req.Projects[0]
	// `profile: demo` is what the stage's dbt_project.yml says, and it is the
	// entry in profiles.yml holding the connection dbt itself uses.
	if p.Profile != "demo" {
		t.Errorf("profile = %q, want the dbt_project.yml profile %q", p.Profile, "demo")
	}
	if p.Target != "prod" {
		t.Errorf("target = %q, want the one the run was given", p.Target)
	}
	if p.Root != dir {
		t.Errorf("root = %q, want %q", p.Root, dir)
	}
	if p.ProfilesDir == "" {
		t.Error("profiles_dir is empty, so a provider has nowhere to look for credentials")
	}

	// Only the external source is asked about: everything else in the graph
	// either has ancestors or is built by a project in the run.
	if len(req.Sources) != 1 || req.Sources[0].UniqueID != externalSource {
		t.Fatalf("expected only the external source, got %+v", req.Sources)
	}
	s := req.Sources[0]
	if s.Database != "demo" || s.Schema != "main" || s.Identifier != "orders" {
		t.Errorf("relation = %s.%s.%s, want demo.main.orders", s.Database, s.Schema, s.Identifier)
	}
	if s.Project != p.Name {
		t.Errorf("source names project %q, which is not in the request", s.Project)
	}
}

func findPython(t *testing.T) string {
	t.Helper()
	// The repository's own environment first: it has PyYAML, which the shared
	// module needs to read profiles.yml.
	if p := filepath.Join(repoRoot(t), ".venv", "bin", "python"); statOK(p) {
		return p
	}
	p, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("no python3 on PATH; the provider contract test needs one")
	}
	return p
}

func statOK(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func repoRoot(t *testing.T) string {
	t.Helper()
	// internal/runner -> repository root.
	abs, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return abs
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	body, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}
