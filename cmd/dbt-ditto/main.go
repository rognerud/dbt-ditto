// Command dbt-ditto propagates dbt column and model documentation down the
// DAG, across projects, and writes it back into schema YAML.
package main

import (
	"flag"
	"fmt"
	"os"
	"runtime"
	"runtime/debug"
	"strings"

	"github.com/rognerud/dbt-ditto/internal/config"
	"github.com/rognerud/dbt-ditto/internal/runner"
)

// Stamped at build time with -ldflags "-X main.version=...". A build produced
// by `go install` has no ldflags, so these fall back to what the Go toolchain
// recorded in the binary.
var (
	version = ""
	commit  = ""
	date    = ""
)

// versionString describes the build as precisely as the binary allows.
func versionString() string {
	v, c, d := version, commit, date
	if info, ok := debug.ReadBuildInfo(); ok {
		if v == "" && info.Main.Version != "" && info.Main.Version != "(devel)" {
			v = info.Main.Version
		}
		for _, s := range info.Settings {
			switch s.Key {
			case "vcs.revision":
				if c == "" {
					c = s.Value
				}
			case "vcs.time":
				if d == "" {
					d = s.Value
				}
			case "vcs.modified":
				if s.Value == "true" {
					v += "-dirty"
				}
			}
		}
	}
	if v == "" {
		v = "dev"
	}
	if c == "unknown" {
		c = ""
	}
	if len(c) > 12 {
		c = c[:12]
	}

	out := "dbt-ditto " + v
	if c != "" {
		out += " (" + c
		if d != "" {
			out += ", " + d
		}
		out += ")"
	}
	return out + "\n" + runtime.Version() + " " + runtime.GOOS + "/" + runtime.GOARCH
}

const usage = `dbt-ditto - dbt documentation inheritance, across projects

Usage:
  dbt-ditto inherit [flags] [project-dir]
  dbt-ditto version

Propagates column documentation down the dbt DAG and writes it back into schema
YAML, reading manifest.json and catalog.json rather than re-parsing the project.
Several projects can take part at once, so a model inherits from a model in
another repository.

Reads dbt_ditto.yml, or a [tool.dbt-ditto] table in pyproject.toml, searched for
upwards from the working directory, unless a project directory is given, in
which case that one project is used with the ` + "`+dbt-osmosis:`" + ` rules already in
its dbt_project.yml.

Flags:
  -c, --config PATH   path to dbt_ditto.yml or pyproject.toml
      --dry-run       report what would change, write nothing
      --check         exit 1 if any file would change, write nothing (for CI)
      --select SPEC   comma-separated: name glob, tag:NAME, path:PREFIX
      --no-organize   never move a model between schema files
  -v, --verbose       list every individual change

      --refresh-sources  run the configured source providers and rewrite the
                         cache. Without it a run reads the cache and connects to
                         nothing, so --check works with no credentials.
      --target NAME      the profiles.yml target providers connect with
                         (default: $DBT_TARGET, else the profile's own default)

Examples:
  dbt-ditto inherit projects/platform
  dbt-ditto inherit --check
  dbt-ditto inherit --select 'stg_*,tag:nightly' --verbose
`

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "dbt-ditto:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		fmt.Print(usage)
		return nil
	}

	switch args[0] {
	case "version", "--version", "-V":
		fmt.Println(versionString())
		return nil
	case "help", "--help", "-h":
		fmt.Print(usage)
		return nil
	case "inherit":
	default:
		return fmt.Errorf("unknown command %q (try `dbt-ditto help`)", args[0])
	}

	fs := flag.NewFlagSet("inherit", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.Usage = func() { fmt.Fprint(os.Stderr, usage) }

	var (
		cfgPath    string
		dryRun     bool
		check      bool
		selectSpec string
		noOrganize bool
		verbose    bool
		refresh    bool
		target     string
	)
	fs.BoolVar(&refresh, "refresh-sources", false, "run the source providers and rewrite the cache")
	fs.StringVar(&target, "target", "", "profiles.yml target for source providers")
	fs.StringVar(&cfgPath, "config", "", "path to dbt_ditto.yml")
	fs.StringVar(&cfgPath, "c", "", "path to dbt_ditto.yml")
	fs.BoolVar(&dryRun, "dry-run", false, "report changes without writing")
	fs.BoolVar(&check, "check", false, "exit 1 if any file would change")
	fs.StringVar(&selectSpec, "select", "", "node selector")
	fs.BoolVar(&noOrganize, "no-organize", false, "never move a model between files")
	fs.BoolVar(&verbose, "verbose", false, "list every change")
	fs.BoolVar(&verbose, "v", false, "list every change")

	// Flags may come after the project directory. Go's flag package stops at the
	// first non-flag argument, so parsing is resumed after each positional:
	// otherwise `dbt-ditto inherit ./project --dry-run` would silently write.
	var positional []string
	rest := args[1:]
	for {
		if err := fs.Parse(rest); err != nil {
			return err
		}
		if fs.NArg() == 0 {
			break
		}
		positional = append(positional, fs.Arg(0))
		rest = fs.Args()[1:]
	}
	if len(positional) > 1 {
		return fmt.Errorf("expected at most one project directory, got %d", len(positional))
	}

	var cfg *config.Config
	var err error
	if len(positional) > 0 {
		cfg = config.Default(positional[0])
	} else {
		cfg, err = config.Load(cfgPath)
		if err != nil {
			return err
		}
	}

	// Notes are about how the projects were assembled (a dbt-loom manifest that
	// could not be reached, say), so they belong before the run, not with the
	// per-column warnings it produces.
	for _, n := range cfg.Notes {
		fmt.Fprintln(os.Stderr, "note:", n)
	}

	// Refreshing reaches the network and rewrites the cache, so it is not
	// something --check should ever do on its own: the point of the cache is
	// that CI reads it rather than dialling out. Asking for both is a mistake
	// worth naming rather than silently resolving.
	if refresh && check {
		return fmt.Errorf("--refresh-sources and --check are contradictory: --check must not reach the warehouse; refresh first, then check")
	}

	opts := runner.Options{
		DryRun:         dryRun || check,
		Check:          check,
		Organize:       !noOrganize,
		Verbose:        verbose,
		RefreshSources: refresh,
		Target:         target,
	}
	if selectSpec != "" {
		opts.Select = strings.Split(selectSpec, ",")
	}

	rep, err := runner.Run(cfg, opts)
	if err != nil {
		return err
	}

	for _, n := range rep.Notes {
		fmt.Fprintln(os.Stderr, "note:", n)
	}

	if verbose {
		for _, c := range rep.Changes {
			fmt.Printf("  %s\n    %s: %s\n", c.Node, c.File, c.Detail)
		}
	}
	for _, f := range rep.FilesWritten {
		verb := "would write"
		if !opts.DryRun {
			verb = "wrote"
		}
		fmt.Printf("%s %s\n", verb, f)
	}
	for _, f := range rep.FilesDeleted {
		verb := "would delete"
		if !opts.DryRun {
			verb = "deleted"
		}
		fmt.Printf("%s %s\n", verb, f)
	}

	// Warnings go to stderr: they are about the project, not about this run, and
	// nothing downstream should have to filter them out of the change list.
	for _, w := range rep.Warnings {
		// A warning about the node itself carries no column, and printing the
		// separator anyway reads as a column named "".
		where := w.Node
		if w.Column != "" {
			where += "." + w.Column
		}
		fmt.Fprintf(os.Stderr, "warning: %s %s\n", where, w.Detail)
	}

	fmt.Printf("%d nodes scanned, %d files changed, %d edits\n",
		rep.NodesScanned, len(rep.FilesWritten)+len(rep.FilesDeleted), len(rep.Changes))
	if len(rep.Warnings) > 0 {
		fmt.Printf("%d warnings\n", len(rep.Warnings))
	}

	if check && (len(rep.FilesWritten) > 0 || len(rep.FilesDeleted) > 0) {
		return fmt.Errorf("documentation is out of date")
	}
	return nil
}
