// Command dbt-ditto propagates dbt column and model documentation down the
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"runtime"
	"runtime/debug"
	"strings"

	"github.com/rognerud/dbt-ditto/internal/config"
	"github.com/rognerud/dbt-ditto/internal/runner"
	"github.com/rognerud/dbt-ditto/internal/scaffold"
)

// Stamped at build time with -ldflags "-X main.version=...".
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
			switch {
			case s.Key == "vcs.revision" && c == "":
				c = s.Value
			case s.Key == "vcs.time" && d == "":
				d = s.Value
			case s.Key == "vcs.modified" && s.Value == "true":
				v += "-dirty"
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
  dbt-ditto init [flags]
  dbt-ditto help config
  dbt-ditto version

Propagates column documentation down the dbt DAG and writes it back into schema
YAML, reading manifest.json and catalog.json rather than re-parsing the project.
Several projects can take part at once, so a model inherits from a model in
another repository.

Reads dbt_ditto.yml, or a [tool.dbt-ditto] table in pyproject.toml, searched for
upwards from the working directory. Given a project directory that one project
is used instead, with the ` + "`+dbt-osmosis:`" + ` rules already in its dbt_project.yml.
With neither, the dbt project the working directory sits in is used, and a note
says so; ` + "`dbt-ditto init`" + ` writes a config for more than one project.

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

init flags:
  -y, --yes           accept the projects it finds, ask nothing
      --force         replace a dbt_ditto.yml that is already there
      --stdout        print the config instead of writing it

Configuration:
  Every key is optional, and the defaults reproduce dbt-osmosis. The sections:

  projects      the dbt projects taking part, and which are only read
  inheritance   what travels down the DAG, and the matching that decides where
  columns       reconciliation against the warehouse catalog: adds, removals,
                data types, case and order
  organize      which schema file a model's entry belongs in
  output        the shape of what is written, YAML comments included
  sources       external programs that document raw tables, and label routing
  tags          folder tags, and the label vocabularies a column resolves to one
  loom          whether dbt_loom.config.yml supplies the upstream manifests

  Every key, its default and what it does:  dbt-ditto help config

Examples:
  dbt-ditto inherit projects/platform
  dbt-ditto inherit --check
  dbt-ditto inherit --select 'stg_*,tag:nightly' --verbose
  dbt-ditto init
  dbt-ditto init --yes --stdout
`

// notes prints remarks about the run, which belong on stderr with the warnings.
func notes(ns []string) {
	for _, n := range ns {
		fmt.Fprintln(os.Stderr, "note:", n)
	}
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "dbt-ditto:", err)
		os.Exit(1)
	}
}

// initConfig writes a starter dbt_ditto.yml. It takes a directory to scan, so
// the command works from outside the tree it is describing.
func initConfig(args []string) error {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.Usage = func() { fmt.Fprint(os.Stderr, usage) }

	var opts scaffold.Options
	fs.BoolVar(&opts.Yes, "yes", false, "accept the projects it finds")
	fs.BoolVar(&opts.Yes, "y", false, "accept the projects it finds")
	fs.BoolVar(&opts.Force, "force", false, "replace an existing config")
	fs.BoolVar(&opts.Stdout, "stdout", false, "print the config instead of writing it")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 1 {
		return fmt.Errorf("expected at most one directory, got %d", fs.NArg())
	}
	opts.Dir = fs.Arg(0)

	// A pipe or a CI log is not something to ask a question on, and a read from it
	// would either block forever or answer the question itself.
	if info, err := os.Stdin.Stat(); err == nil {
		opts.Interactive = info.Mode()&os.ModeCharDevice != 0
	}
	return scaffold.Run(opts)
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
		// `help config` is the whole settings reference. It is a subcommand rather
		// than part of the usage text because sixty keys with an explanation each
		// would bury the four flags somebody typing --help is usually after.
		if len(args) > 1 && (args[1] == "config" || args[1] == "configuration") {
			return config.SettingsHelp(os.Stdout)
		}
		fmt.Print(usage)
		return nil
	case "config":
		return config.SettingsHelp(os.Stdout)
	case "init":
		return initConfig(args[1:])
	case "inherit":
	default:
		return fmt.Errorf("unknown command %q (try `dbt-ditto help`)", args[0])
	}

	fs := flag.NewFlagSet("inherit", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.Usage = func() { fmt.Fprint(os.Stderr, usage) }

	// Bound straight into the options the runner takes, so no flag is copied twice.
	// The short spellings are aliases, and share their variable.
	var opts runner.Options
	var cfgPath, selectSpec string
	var noOrganize bool
	fs.BoolVar(&opts.RefreshSources, "refresh-sources", false, "run the source providers and rewrite the cache")
	fs.StringVar(&opts.Target, "target", "", "profiles.yml target for source providers")
	fs.BoolVar(&opts.DryRun, "dry-run", false, "report changes without writing")
	fs.BoolVar(&opts.Check, "check", false, "exit 1 if any file would change")
	fs.StringVar(&selectSpec, "select", "", "node selector")
	fs.BoolVar(&noOrganize, "no-organize", false, "never move a model between files")
	fs.StringVar(&cfgPath, "config", "", "path to dbt_ditto.yml")
	fs.StringVar(&cfgPath, "c", "", "path to dbt_ditto.yml")
	fs.BoolVar(&opts.Verbose, "verbose", false, "list every change")
	fs.BoolVar(&opts.Verbose, "v", false, "list every change")

	// Flags may come after the project directory, and Go's flag package stops at
	// the first non-flag argument, so parsing resumes after each positional:
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
		cfg, err = config.LoadOrProject(cfgPath)
		if err != nil {
			return err
		}
	}

	// Notes are about how the projects were assembled, so they belong before the
	// run rather than with the per-column warnings it produces.
	notes(cfg.Notes)

	// Refreshing reaches the network and rewrites the cache, which is not something
	// --check should do: the point of the cache is that CI reads it rather than
	// dialling out. Asking for both is a mistake worth naming.
	if opts.RefreshSources && opts.Check {
		return errors.New("--refresh-sources and --check are contradictory: --check must not reach the warehouse; refresh first, then check")
	}

	opts.DryRun = opts.DryRun || opts.Check
	opts.Organize = !noOrganize
	if selectSpec != "" {
		opts.Select = strings.Split(selectSpec, ",")
	}

	rep, err := runner.Run(cfg, opts)
	if err != nil {
		return err
	}

	notes(rep.Notes)

	if opts.Verbose {
		for _, c := range rep.Changes {
			fmt.Printf("  %s\n    %s: %s\n", c.Node, c.File, c.Detail)
		}
	}
	list := func(files []string, done, would string) {
		verb := done
		if opts.DryRun {
			verb = would
		}
		for _, f := range files {
			fmt.Printf("%s %s\n", verb, f)
		}
	}
	list(rep.FilesWritten, "wrote", "would write")
	list(rep.FilesDeleted, "deleted", "would delete")

	// Warnings go to stderr: they are about the project, not about this run.
	for _, w := range rep.Warnings {
		// A warning about the node itself carries no column, and the separator would
		// read as a column named "".
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

	if opts.Check && (len(rep.FilesWritten) > 0 || len(rep.FilesDeleted) > 0) {
		return errors.New("documentation is out of date")
	}
	return nil
}
