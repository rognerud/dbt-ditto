package features

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/cucumber/godog"
)

// invocation is one run of the real binary: what the user typed, and everything
// they would see afterwards.
type invocation struct {
	args   []string
	stdout string
	stderr string
	code   int
}

// binary builds cmd/dbt-ditto once per test process and returns its path. The
// documentation's scenarios are about the command a user types, so they run the
// command rather than the package behind it: the flag parsing, the exit code and
// what lands on which stream are part of what the docs promise.
var binary = sync.OnceValues(func() (string, error) {
	dir, err := os.MkdirTemp("", "ditto-bin")
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, "dbt-ditto")
	build := exec.Command("go", "build", "-o", path, "github.com/rognerud/dbt-ditto/cmd/dbt-ditto")
	if out, err := build.CombinedOutput(); err != nil {
		return "", fmt.Errorf("build dbt-ditto: %w\n%s", err, out)
	}
	return path, nil
})

// runCLI runs `dbt-ditto …` in the scenario's project directory. The command is
// written as it would be typed, so the step takes the whole line and splits it.
func (w *world) runCLI(command string) error {
	fields := strings.Fields(strings.TrimSpace(command))
	if len(fields) == 0 {
		return errors.New("no command was given")
	}
	if fields[0] != "dbt-ditto" {
		return fmt.Errorf("only dbt-ditto can be run here, got %q", fields[0])
	}

	path, err := binary()
	if err != nil {
		return err
	}
	if len(w.runs) == 0 {
		if err := w.writeProject(false); err != nil {
			return err
		}
	}

	args := unquote(fields[1:])
	cmd := exec.Command(path, args...)
	cmd.Dir = w.dir
	// A project of its own, with nothing inherited from the machine running the
	// suite: no ambient dbt target, and no config found by walking out of the
	// temporary directory.
	cmd.Env = append(os.Environ(), "DBT_TARGET=", "DBT_LOOM_CONFIG=")
	var out, errOut strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	runErr := cmd.Run()

	inv := &invocation{args: args, stdout: out.String(), stderr: errOut.String()}
	var exit *exec.ExitError
	switch {
	case runErr == nil:
	case errors.As(runErr, &exit):
		inv.code = exit.ExitCode()
	default:
		return fmt.Errorf("run %s: %w", command, runErr)
	}
	w.cli = inv

	// The report belongs to the in-process steps; a command run this way is
	// observed through its output and the tree it leaves.
	w.rep = nil
	snap, err := w.snapshot()
	if err != nil {
		return err
	}
	w.runs = append(w.runs, snap)
	return nil
}

// unquote strips the quotes a user needs at a shell prompt but a direct exec
// does not, so `--select 'stg_*'` reaches the binary as one argument.
func unquote(args []string) []string {
	out := make([]string, 0, len(args))
	for _, a := range args {
		for _, q := range []string{"'", `"`} {
			if len(a) > 1 && strings.HasPrefix(a, q) && strings.HasSuffix(a, q) {
				a = a[1 : len(a)-1]
				break
			}
		}
		out = append(out, a)
	}
	return out
}

func (w *world) invocation() (*invocation, error) {
	if w.cli == nil {
		return nil, errors.New("no command has been run yet")
	}
	return w.cli, nil
}

func (w *world) exitCodeIs(want int) error {
	inv, err := w.invocation()
	if err != nil {
		return err
	}
	if inv.code != want {
		return fmt.Errorf("dbt-ditto %s exited %d, expected %d:\n%s%s",
			strings.Join(inv.args, " "), inv.code, want, inv.stdout, inv.stderr)
	}
	return nil
}

func (w *world) stdoutContains(want *godog.DocString) error {
	inv, err := w.invocation()
	if err != nil {
		return err
	}
	return contains(inv.stdout, "stdout", want.Content)
}

func (w *world) stdoutContainsLine(want string) error {
	inv, err := w.invocation()
	if err != nil {
		return err
	}
	return contains(inv.stdout, "stdout", want)
}

func (w *world) stdoutLacks(want string) error {
	inv, err := w.invocation()
	if err != nil {
		return err
	}
	if strings.Contains(inv.stdout, want) {
		return fmt.Errorf("stdout contains %q, and should not:\n%s", want, inv.stdout)
	}
	return nil
}

func (w *world) stderrContains(want *godog.DocString) error {
	inv, err := w.invocation()
	if err != nil {
		return err
	}
	return contains(inv.stderr, "stderr", want.Content)
}

func (w *world) stderrContainsLine(want string) error {
	inv, err := w.invocation()
	if err != nil {
		return err
	}
	return contains(inv.stderr, "stderr", want)
}

func (w *world) stderrSilent() error {
	inv, err := w.invocation()
	if err != nil {
		return err
	}
	if strings.TrimSpace(inv.stderr) != "" {
		return fmt.Errorf("stderr is not empty:\n%s", inv.stderr)
	}
	return nil
}

// contains checks a stream line by line, each line trimmed, so a doc block reads
// as the output it quotes rather than as one long string.
func contains(stream, name, want string) error {
	for _, line := range strings.Split(strings.TrimSpace(want), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if !strings.Contains(stream, line) {
			return fmt.Errorf("%s does not contain %q:\n%s", name, line, stream)
		}
	}
	return nil
}
