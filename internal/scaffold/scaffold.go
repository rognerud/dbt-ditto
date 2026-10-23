// Package scaffold writes a starter dbt_ditto.yml from the dbt projects on
// disk. It answers the one question a single project directory cannot — which
// projects take part, and which of them are only read — and leaves every other
// setting at its default, because the defaults are what keep a first run
// byte-identical to dbt-osmosis.
package scaffold

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/rognerud/dbt-ditto/internal/config"
	"gopkg.in/yaml.v3"
)

// Options is one `dbt-ditto init` invocation.
type Options struct {
	// Dir is scanned for projects and is where the config is written.
	Dir string
	// Yes accepts every detection without asking, which is what a pipeline wants.
	Yes bool
	// Force overwrites a config that is already there.
	Force bool
	// Stdout prints the config instead of writing it, so it can be reviewed or
	// redirected somewhere this command would not have put it.
	Stdout bool

	// Interactive says whether there is a terminal to prompt on. Without one, and
	// without Yes, the command refuses rather than hanging on a read that will
	// never return.
	Interactive bool

	In  io.Reader
	Out io.Writer
}

// MaxDepth bounds the downward scan. Deeper than this is not a monorepo layout,
// it is `dbt_packages` or a stray checkout, and walking it is a surprise.
const MaxDepth = 4

// skipDirs are never descended into: dbt's own output, dependency trees, and
// anything that would make the scan report a vendored project as one of yours.
var skipDirs = map[string]bool{
	".git":          true,
	"target":        true,
	"dbt_packages":  true,
	"dbt_modules":   true,
	"logs":          true,
	"node_modules":  true,
	".venv":         true,
	"venv":          true,
	".tox":          true,
	"site-packages": true,
}

// Project is one dbt project the scan found.
type Project struct {
	// Path is relative to the scanned directory, in slashes, "." for the
	// directory itself. It is written to the config as-is, so a config that moves
	// with its repository keeps working.
	Path string
	// Name is dbt_project.yml's `name:`, shown so a user recognises the project
	// by what dbt calls it rather than by its directory.
	Name string
	// Loom is the relative path of this project's dbt_loom.config.yml, empty when
	// it has none.
	Loom string

	// Include and Upstream are what the prompts decide.
	Include  bool
	Upstream bool
}

// Run detects, asks, and writes.
func Run(o Options) error {
	if o.Out == nil {
		o.Out = os.Stdout
	}
	if o.In == nil {
		o.In = os.Stdin
	}
	dir := o.Dir
	if dir == "" {
		dir = "."
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return err
	}

	dest := filepath.Join(abs, config.Filenames[0])
	if !o.Stdout {
		if existing, found := existingConfig(abs); found && !o.Force {
			return fmt.Errorf("%s already exists; pass --force to replace it, or --stdout to print what init would write",
				config.Rel(abs, existing))
		}
	}

	found, err := Detect(abs)
	if err != nil {
		return err
	}
	if len(found) == 0 {
		return fmt.Errorf("no %s found in %s or below it (searched %d levels down): run init from the directory your dbt projects live in",
			config.ProjectFilename, dir, MaxDepth)
	}

	loom := true
	if !o.Yes {
		if !o.Interactive {
			return errors.New("init has no terminal to ask on: pass --yes to accept what it finds, or run it from a terminal")
		}
		if loom, err = ask(o, found); err != nil {
			return err
		}
	} else {
		report(o.Out, found)
		for i := range found {
			found[i].Include = true
		}
	}

	kept := make([]Project, 0, len(found))
	for _, p := range found {
		if p.Include {
			kept = append(kept, p)
		}
	}
	if len(kept) == 0 {
		return errors.New("no projects were kept, so there is nothing to configure")
	}

	body := Render(kept, loom, anyLoom(kept))
	if o.Stdout {
		_, err := io.WriteString(o.Out, body)
		return err
	}
	if err := os.WriteFile(dest, []byte(body), 0o644); err != nil {
		return err
	}
	fmt.Fprintf(o.Out, "wrote %s (%s)\n", config.Rel(abs, dest), count(len(kept), "project"))
	return nil
}

// Detect walks dir downwards for dbt projects, nearest first.
func Detect(dir string) ([]Project, error) {
	var out []Project
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			// A directory that cannot be read is not a reason to abandon the scan:
			// a monorepo may well contain one this user has no business in.
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if !d.IsDir() {
			return nil
		}
		rel, ok := relative(dir, path)
		if !ok {
			return nil
		}
		if rel != "." {
			if skipDirs[d.Name()] || strings.HasPrefix(d.Name(), ".") {
				return fs.SkipDir
			}
			if depth(rel) > MaxDepth {
				return fs.SkipDir
			}
		}
		if !exists(filepath.Join(path, config.ProjectFilename)) {
			return nil
		}
		p := Project{Path: filepath.ToSlash(rel), Name: projectName(path)}
		if exists(filepath.Join(path, LoomFilename)) {
			p.Loom = filepath.ToSlash(filepath.Join(rel, LoomFilename))
		}
		out = append(out, p)
		// A dbt project does not contain another one; what looks like it does is
		// a package directory this scan has no business in.
		return fs.SkipDir
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

// LoomFilename duplicates dbt-loom's config name rather than exporting it from
// the config package, where it is an implementation detail of the upstream scan.
const LoomFilename = "dbt_loom.config.yml"

// Render turns the kept projects into the file. The comments are the point: the
// next person to open this file should not have to find the documentation to
// learn that everything absent is a default.
func Render(projects []Project, loom, sawLoom bool) string {
	var b strings.Builder
	b.WriteString("# dbt-ditto. Written by `dbt-ditto init`.\n")
	b.WriteString("#\n")
	b.WriteString("# Only the projects are set here. Every other setting is at its default,\n")
	b.WriteString("# which is dbt-osmosis' behaviour byte for byte:\n")
	b.WriteString("# https://github.com/rognerud/dbt-ditto/blob/main/docs/configuration.md\n")
	b.WriteString("\n")
	b.WriteString("projects:\n")
	for _, p := range projects {
		fmt.Fprintf(&b, "  - path: %s", quotePath(p.Path))
		// dbt decides a project's name, so it is a comment rather than a key: the
		// config has nowhere to put it, and a reader still wants to see it.
		if p.Name != "" {
			fmt.Fprintf(&b, "  # %s", p.Name)
		}
		b.WriteString("\n")
		if p.Upstream {
			b.WriteString("    # Read for its documentation, never written to.\n")
			b.WriteString("    upstream: true\n")
		}
	}
	switch {
	case !sawLoom:
	case loom:
		b.WriteString("\n# Upstream manifests are read from dbt_loom.config.yml, so they are not\n")
		b.WriteString("# listed twice. Set `loom: false` to list them here instead.\n")
	default:
		b.WriteString("\n# dbt_loom.config.yml is ignored: add each upstream manifest above as\n")
		b.WriteString("# `- manifest: path/to/manifest.json` with `upstream: true`.\n")
		b.WriteString("loom: false\n")
	}
	return b.String()
}

// ask runs the prompts, and reports whether dbt-loom's config should be read.
func ask(o Options, found []Project) (bool, error) {
	in := bufio.NewReader(o.In)
	report(o.Out, found)

	all := true
	if len(found) > 1 {
		var err error
		if all, err = yesNo(in, o.Out, "  include all of them?", true); err != nil {
			return false, err
		}
	}
	for i := range found {
		found[i].Include = all
		if !all {
			yes, err := yesNo(in, o.Out, "  include "+found[i].Path+"?", true)
			if err != nil {
				return false, err
			}
			found[i].Include = yes
		}
	}

	// Which projects are only read is the one thing a directory cannot imply, and
	// getting it wrong writes to a repository the user does not own.
	if countIncluded(found) > 1 {
		for i := range found {
			if !found[i].Include {
				continue
			}
			yes, err := yesNo(in, o.Out, "  is "+found[i].Path+" upstream (read, never written to)?", false)
			if err != nil {
				return false, err
			}
			found[i].Upstream = yes
		}
	}

	loom := true
	for _, p := range found {
		if !p.Include || p.Loom == "" {
			continue
		}
		fmt.Fprintf(o.Out, "Found %s\n", p.Loom)
		yes, err := yesNo(in, o.Out, "  upstream projects will be read from loom?", true)
		if err != nil {
			return false, err
		}
		loom = yes
		break
	}
	return loom, nil
}

// report prints what the scan found, before anything is asked about it.
func report(out io.Writer, found []Project) {
	fmt.Fprintf(out, "Found %s\n", count(len(found), "dbt project"))
	for _, p := range found {
		label := p.Path
		if p.Name != "" {
			label += "  (" + p.Name + ")"
		}
		fmt.Fprintf(out, "  [x] %s\n", label)
	}
}

// yesNo asks once and takes the default on an empty line. EOF is the default
// too: a user who closed the input has not said no to each question in turn.
func yesNo(in *bufio.Reader, out io.Writer, question string, def bool) (bool, error) {
	hint := " [y/N] "
	if def {
		hint = " [Y/n] "
	}
	fmt.Fprint(out, question+hint)
	line, err := in.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return false, err
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true, nil
	case "n", "no":
		return false, nil
	default:
		return def, nil
	}
}

// existingConfig reports a config this tool owns already sitting in dir. A
// pyproject.toml is only one when it carries the table, which is the same rule
// the upward search applies.
func existingConfig(dir string) (string, bool) {
	for _, name := range config.Filenames {
		p := filepath.Join(dir, name)
		if !exists(p) {
			continue
		}
		if name == config.PyprojectFilename && !hasTable(p) {
			continue
		}
		return p, true
	}
	return "", false
}

// hasTable reports whether a pyproject.toml carries dbt-ditto's table, by the
// crude test that is enough here: the config package decides for real.
func hasTable(path string) bool {
	raw, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	return strings.Contains(string(raw), "["+config.PyprojectTable+"]")
}

// projectName reads `name:` out of dbt_project.yml, and says nothing when it
// cannot: the name is a label on a prompt, not something the run depends on.
func projectName(root string) string {
	raw, err := os.ReadFile(filepath.Join(root, config.ProjectFilename))
	if err != nil {
		return ""
	}
	var p struct {
		Name string `yaml:"name"`
	}
	if err := yaml.Unmarshal(raw, &p); err != nil {
		return ""
	}
	return p.Name
}

func anyLoom(projects []Project) bool {
	for _, p := range projects {
		if p.Loom != "" {
			return true
		}
	}
	return false
}

func countIncluded(projects []Project) int {
	n := 0
	for _, p := range projects {
		if p.Include {
			n++
		}
	}
	return n
}

// exists reports a path being there, which is all the scan asks: why it is not
// readable is the walk's business, and it skips what it cannot read.
func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// relative reports path's position under base, and whether it has one at all.
func relative(base, path string) (string, bool) {
	rel, err := filepath.Rel(base, path)
	if err != nil {
		return "", false
	}
	return rel, true
}

// depth counts the path segments in a relative path.
func depth(rel string) int {
	if rel == "." {
		return 0
	}
	return len(strings.Split(filepath.ToSlash(rel), "/"))
}

// quotePath keeps `.` a string rather than something a YAML reader has to think
// about, and quotes anything else that would not round-trip bare.
func quotePath(p string) string {
	if p == "." || strings.ContainsAny(p, ":#'\"{}[]&*?|<>=!%@`,") || strings.TrimSpace(p) != p {
		return strconv.Quote(p)
	}
	return p
}

// count renders "1 project" and "2 projects", so no message says "1 projects".
func count(n int, noun string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, noun)
	}
	return fmt.Sprintf("%d %ss", n, noun)
}
