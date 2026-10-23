package scaffold

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rognerud/dbt-ditto/internal/config"
	"gopkg.in/yaml.v3"
)

// tree writes a set of files, each with its content, under a fresh directory.
func tree(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for rel, body := range files {
		path := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// monorepo is the layout the command exists for: two projects side by side, one
// of them using dbt-loom, and two directories that must not be mistaken for a
// project of the user's own.
func monorepo(t *testing.T) string {
	t.Helper()
	return tree(t, map[string]string{
		"projects/platform/dbt_project.yml":  "name: platform\n",
		"projects/analytics/dbt_project.yml": "name: analytics\n",
		"projects/analytics/dbt_loom.config.yml": "manifests:\n  - name: platform\n    type: file\n" +
			"    config:\n      path: ../platform/target/manifest.json\n",
		"projects/analytics/dbt_packages/vendored/dbt_project.yml": "name: vendored\n",
		// Where the loom config above says the upstream manifest is: the upstream
		// scan stats it, and skips the entry with a note when it is not there.
		"projects/platform/target/manifest.json": "{}",
		".hidden/project/dbt_project.yml":        "name: hidden\n",
	})
}

// hasLine reports a line of its own, so an assertion about `loom: false` is not
// satisfied by the comment that mentions it.
func hasLine(body, want string) bool {
	for _, line := range strings.Split(body, "\n") {
		if strings.TrimSpace(line) == want {
			return true
		}
	}
	return false
}

func TestDetectFindsEveryProjectOnce(t *testing.T) {
	found, err := Detect(monorepo(t))
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, p := range found {
		paths = append(paths, p.Path)
	}
	want := []string{"projects/analytics", "projects/platform"}
	if strings.Join(paths, ",") != strings.Join(want, ",") {
		t.Fatalf("detected %v, want %v", paths, want)
	}
	if found[0].Name != "analytics" || found[1].Name != "platform" {
		t.Fatalf("names are %q and %q", found[0].Name, found[1].Name)
	}
}

// A project vendored into dbt_packages is dbt's dependency, not one of the
// user's, and listing it would write to somebody else's repository.
func TestDetectSkipsPackagesAndHiddenDirectories(t *testing.T) {
	found, err := Detect(monorepo(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range found {
		if strings.Contains(p.Path, "dbt_packages") || strings.HasPrefix(p.Path, ".") {
			t.Fatalf("detected %q, which is not the user's project", p.Path)
		}
	}
}

func TestDetectReportsTheLoomConfig(t *testing.T) {
	found, err := Detect(monorepo(t))
	if err != nil {
		t.Fatal(err)
	}
	if got := found[0].Loom; got != "projects/analytics/dbt_loom.config.yml" {
		t.Fatalf("loom config is %q", got)
	}
	if got := found[1].Loom; got != "" {
		t.Fatalf("platform has no loom config, got %q", got)
	}
}

// The directory the command is run in is a project in its own right, which is
// the single-project case `inherit .` already handles.
func TestDetectFindsTheDirectoryItself(t *testing.T) {
	dir := tree(t, map[string]string{"dbt_project.yml": "name: solo\n"})
	found, err := Detect(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 1 || found[0].Path != "." {
		t.Fatalf("detected %+v", found)
	}
}

func TestDetectStopsAtMaxDepth(t *testing.T) {
	deep := strings.Repeat("a/", MaxDepth+1) + "dbt_project.yml"
	found, err := Detect(tree(t, map[string]string{deep: "name: deep\n"}))
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 0 {
		t.Fatalf("detected %+v, which is deeper than %d levels", found, MaxDepth)
	}
}

// The whole point of writing a config is that the tool can then read it.
func TestTheWrittenConfigLoads(t *testing.T) {
	dir := monorepo(t)
	if err := Run(Options{Dir: dir, Yes: true, Out: &strings.Builder{}}); err != nil {
		t.Fatal(err)
	}
	c, err := config.Load(filepath.Join(dir, config.Filenames[0]))
	if err != nil {
		t.Fatal(err)
	}
	// Two projects configured, plus the upstream manifest loom declares.
	if len(c.Projects) != 3 {
		t.Fatalf("loaded %d projects: %+v", len(c.Projects), c.Projects)
	}
	var upstreams int
	for _, p := range c.Projects {
		if p.Upstream {
			upstreams++
		}
	}
	if upstreams != 1 {
		t.Fatalf("%d upstream projects, want the one loom declares", upstreams)
	}
}

func TestExistingConfigIsNotReplacedWithoutForce(t *testing.T) {
	dir := monorepo(t)
	path := filepath.Join(dir, config.Filenames[0])
	if err := os.WriteFile(path, []byte("projects:\n  - path: .\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := Run(Options{Dir: dir, Yes: true, Out: &strings.Builder{}})
	if err == nil {
		t.Fatal("init replaced an existing config")
	}
	if !strings.Contains(err.Error(), "--force") {
		t.Fatalf("the error does not say how to proceed: %v", err)
	}
	raw, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(raw) != "projects:\n  - path: .\n" {
		t.Fatalf("the existing config was rewritten:\n%s", raw)
	}

	if err := Run(Options{Dir: dir, Yes: true, Force: true, Out: &strings.Builder{}}); err != nil {
		t.Fatal(err)
	}
	raw, readErr = os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !strings.Contains(string(raw), "projects/platform") {
		t.Fatalf("--force did not replace the config:\n%s", raw)
	}
}

// A pyproject.toml is only a config when it carries the table, the same rule
// the upward search applies.
func TestPyprojectWithoutTheTableIsNotAConfig(t *testing.T) {
	dir := tree(t, map[string]string{
		"dbt_project.yml": "name: solo\n",
		"pyproject.toml":  "[project]\nname = \"something-else\"\n",
	})
	if err := Run(Options{Dir: dir, Yes: true, Out: &strings.Builder{}}); err != nil {
		t.Fatalf("a pyproject.toml without the table blocked init: %v", err)
	}

	dir = tree(t, map[string]string{
		"dbt_project.yml": "name: solo\n",
		"pyproject.toml":  "[tool.dbt-ditto]\nprojects = [{ path = \".\" }]\n",
	})
	err := Run(Options{Dir: dir, Yes: true, Out: &strings.Builder{}})
	if err == nil || !strings.Contains(err.Error(), "pyproject.toml") {
		t.Fatalf("a pyproject.toml carrying the table should be respected, got %v", err)
	}
}

func TestStdoutWritesNothing(t *testing.T) {
	dir := monorepo(t)
	var out strings.Builder
	if err := Run(Options{Dir: dir, Yes: true, Stdout: true, Out: &out}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, config.Filenames[0])); err == nil {
		t.Fatal("--stdout wrote a config")
	}
	if !strings.Contains(out.String(), "projects:") {
		t.Fatalf("--stdout printed no config:\n%s", out.String())
	}
}

// Without a terminal and without --yes there is nothing to prompt on, and a
// read would block for as long as the pipeline let it.
func TestNoTerminalAndNoYesIsRefused(t *testing.T) {
	err := Run(Options{Dir: monorepo(t), Out: &strings.Builder{}})
	if err == nil {
		t.Fatal("init prompted with no terminal")
	}
	if !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("the error does not say how to proceed: %v", err)
	}
}

func TestADirectoryWithNoProjectIsAnError(t *testing.T) {
	err := Run(Options{Dir: t.TempDir(), Yes: true, Out: &strings.Builder{}})
	if err == nil || !strings.Contains(err.Error(), config.ProjectFilename) {
		t.Fatalf("expected the error to name dbt_project.yml, got %v", err)
	}
}

// The prompts: declining a project keeps it out of the file, and declining loom
// writes the switch that turns the upstream scan off.
func TestPromptsDecideTheProjectsAndLoom(t *testing.T) {
	dir := monorepo(t)
	var out strings.Builder
	in := strings.NewReader(strings.Join([]string{
		"n",   // not all of them
		"n",   // skip projects/analytics
		"y",   // keep projects/platform
		"n\n", // and loom is never asked, analytics being out
	}, "\n"))

	err := Run(Options{Dir: dir, Interactive: true, In: in, Out: &out})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, config.Filenames[0]))
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	if strings.Contains(body, "projects/analytics") {
		t.Fatalf("a declined project was written:\n%s", body)
	}
	if !strings.Contains(body, "projects/platform") {
		t.Fatalf("an accepted project was not written:\n%s", body)
	}
	if !strings.Contains(out.String(), "include projects/analytics?") {
		t.Fatalf("the prompt was not asked:\n%s", out.String())
	}
}

func TestDecliningLoomWritesTheSwitch(t *testing.T) {
	dir := monorepo(t)
	// Keep both projects, mark neither upstream, and decline loom.
	in := strings.NewReader("y\nn\nn\nn\n")
	if err := Run(Options{Dir: dir, Interactive: true, In: in, Out: &strings.Builder{}}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, config.Filenames[0]))
	if err != nil {
		t.Fatal(err)
	}
	if !hasLine(string(raw), "loom: false") {
		t.Fatalf("declining loom wrote no switch:\n%s", raw)
	}

	c, err := config.Load(filepath.Join(dir, config.Filenames[0]))
	if err != nil {
		t.Fatal(err)
	}
	// With loom off, the upstream manifest it declares is not attached.
	if len(c.Projects) != 2 {
		t.Fatalf("loaded %d projects with loom off: %+v", len(c.Projects), c.Projects)
	}
}

// An upstream project is read and never written to, so the answer has to reach
// the file: getting it wrong edits a repository the user does not own.
func TestUpstreamAnswerIsWritten(t *testing.T) {
	dir := monorepo(t)
	// All of them; analytics not upstream, platform upstream; loom declined.
	in := strings.NewReader("y\nn\ny\nn\n")
	if err := Run(Options{Dir: dir, Interactive: true, In: in, Out: &strings.Builder{}}); err != nil {
		t.Fatal(err)
	}
	c, err := config.Load(filepath.Join(dir, config.Filenames[0]))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range c.Projects {
		if strings.HasSuffix(p.Path, "platform") != p.Upstream {
			t.Fatalf("project %q has upstream=%v", p.Path, p.Upstream)
		}
	}
}

// A closed input is not an answer of "no" to every question in turn: the
// defaults stand, which is what --yes would have done.
func TestEOFTakesTheDefaults(t *testing.T) {
	dir := monorepo(t)
	if err := Run(Options{Dir: dir, Interactive: true, In: strings.NewReader(""), Out: &strings.Builder{}}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, config.Filenames[0]))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"projects/analytics", "projects/platform"} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("%s is missing from the default answer:\n%s", want, raw)
		}
	}
	if hasLine(string(raw), "loom: false") {
		t.Fatalf("loom defaults to on:\n%s", raw)
	}
}

func TestRenderQuotesTheCurrentDirectory(t *testing.T) {
	body := Render([]Project{{Path: ".", Name: "solo"}}, true, false)
	if !strings.Contains(body, `- path: "."`) {
		t.Fatalf("the path is not quoted:\n%s", body)
	}
	var c struct {
		Projects []struct {
			Path string `yaml:"path"`
		} `yaml:"projects"`
	}
	if err := yaml.Unmarshal([]byte(body), &c); err != nil {
		t.Fatal(err)
	}
	if len(c.Projects) != 1 || c.Projects[0].Path != "." {
		t.Fatalf("parsed %+v", c.Projects)
	}
}
