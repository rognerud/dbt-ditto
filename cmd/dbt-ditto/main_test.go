package main

import (
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// fixture copies testdata/projects/analytics into a temporary directory, so a
// test can run the real thing and still assert on what was written.
func fixture(t *testing.T) string {
	t.Helper()
	_, self, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate the test file")
	}
	src := filepath.Join(filepath.Dir(self), "..", "..", "testdata", "projects", "analytics")
	dst := filepath.Join(t.TempDir(), "analytics")

	err := filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, b, 0o644)
	})
	if err != nil {
		t.Fatalf("copy fixture: %v", err)
	}
	return dst
}

// snapshot records every file under dir and its contents.
func snapshot(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		out[rel] = string(b)
		return nil
	})
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	return out
}

// capture runs fn with stdout redirected, and returns what it printed. run()
// writes to os.Stdout directly, which is the behaviour being tested.
func capture(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	saved := os.Stdout
	os.Stdout = w

	done := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()

	runErr := fn()

	os.Stdout = saved
	_ = w.Close()
	out := <-done
	_ = r.Close()
	return out, runErr
}

func TestNoArgumentsPrintsUsage(t *testing.T) {
	out, err := capture(t, func() error { return run(nil) })
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(out, "dbt-ditto inherit") {
		t.Errorf("output = %q, want the usage text", out)
	}
}

func TestHelpAndVersionSpellings(t *testing.T) {
	for _, arg := range []string{"help", "--help", "-h"} {
		out, err := capture(t, func() error { return run([]string{arg}) })
		if err != nil {
			t.Errorf("%s: %v", arg, err)
		}
		if !strings.Contains(out, "Usage:") {
			t.Errorf("%s printed %q, want the usage text", arg, out)
		}
	}
	for _, arg := range []string{"version", "--version", "-V"} {
		out, err := capture(t, func() error { return run([]string{arg}) })
		if err != nil {
			t.Errorf("%s: %v", arg, err)
		}
		if !strings.Contains(out, "dbt-ditto") {
			t.Errorf("%s printed %q, want a version line", arg, out)
		}
	}
}

// A typo must name itself rather than being read as a project directory.
func TestUnknownCommandIsRejected(t *testing.T) {
	_, err := capture(t, func() error { return run([]string{"inhert", "."}) })
	if err == nil {
		t.Fatal("an unknown command succeeded")
	}
	if !strings.Contains(err.Error(), `unknown command "inhert"`) {
		t.Errorf("error = %v, want it to quote the command", err)
	}
}

// Go's flag package stops at the first non-flag argument. If parsing did not
// resume afterwards, this invocation would silently write to the project.
func TestFlagsAfterTheProjectDirectoryStillApply(t *testing.T) {
	dir := fixture(t)
	before := snapshot(t, dir)

	out, err := capture(t, func() error { return run([]string{"inherit", dir, "--dry-run"}) })
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(out, "would write") {
		t.Errorf("output = %q, want --dry-run to have been honoured", out)
	}
	for name, was := range before {
		if now := snapshot(t, dir)[name]; now != was {
			t.Errorf("%s was written despite --dry-run following the directory", name)
		}
	}
}

// The same trap, for a flag that changes the exit status.
func TestCheckAfterTheProjectDirectoryStillApplies(t *testing.T) {
	dir := fixture(t)
	_, err := capture(t, func() error { return run([]string{"inherit", dir, "--check"}) })
	if err == nil {
		t.Fatal("--check passed on a project with stale documentation")
	}
	if !strings.Contains(err.Error(), "out of date") {
		t.Errorf("error = %v, want it to say the documentation is out of date", err)
	}
}

func TestMoreThanOneProjectDirectoryIsRejected(t *testing.T) {
	_, err := capture(t, func() error { return run([]string{"inherit", "a", "b"}) })
	if err == nil {
		t.Fatal("two project directories were accepted")
	}
	if !strings.Contains(err.Error(), "at most one project directory") {
		t.Errorf("error = %v", err)
	}
}

// --check exists so CI needs no warehouse credentials; --refresh-sources dials
// out. Asking for both is a mistake worth naming rather than silently resolving.
func TestRefreshSourcesAndCheckAreRejectedTogether(t *testing.T) {
	dir := fixture(t)
	_, err := capture(t, func() error {
		return run([]string{"inherit", dir, "--refresh-sources", "--check"})
	})
	if err == nil {
		t.Fatal("--refresh-sources --check was accepted")
	}
	if !strings.Contains(err.Error(), "contradictory") {
		t.Errorf("error = %v, want it to name the contradiction", err)
	}
}

func TestUnknownFlagIsAnError(t *testing.T) {
	_, err := capture(t, func() error { return run([]string{"inherit", "--nope"}) })
	if err == nil {
		t.Fatal("an unknown flag was accepted")
	}
}

// --verbose lists every edit; without it only the file lines and the summary
// are printed.
func TestVerboseListsEveryEdit(t *testing.T) {
	dir := fixture(t)
	quiet, err := capture(t, func() error { return run([]string{"inherit", dir, "--dry-run"}) })
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	loud, err := capture(t, func() error { return run([]string{"inherit", dir, "--dry-run", "-v"}) })
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(loud) <= len(quiet) {
		t.Errorf("--verbose printed %d bytes, plain printed %d; want more", len(loud), len(quiet))
	}
	if !strings.Contains(loud, "+ column") {
		t.Errorf("--verbose output names no column:\n%s", loud)
	}
}

// A selector that matches nothing is not an error: it scans nothing and says so.
func TestSelectNarrowsTheRun(t *testing.T) {
	dir := fixture(t)
	out, err := capture(t, func() error {
		return run([]string{"inherit", dir, "--dry-run", "--select", "no_such_model_*"})
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(out, "0 nodes scanned") {
		t.Errorf("output = %q, want nothing to have been scanned", out)
	}
}

// A missing project directory has to fail rather than quietly scanning nothing.
func TestAMissingProjectIsAnError(t *testing.T) {
	_, err := capture(t, func() error {
		return run([]string{"inherit", filepath.Join(t.TempDir(), "absent")})
	})
	if err == nil {
		t.Fatal("a missing project directory was accepted")
	}
}

func TestVersionStringDescribesTheBuild(t *testing.T) {
	if got := versionString(); !strings.HasPrefix(got, "dbt-ditto") {
		t.Errorf("versionString() = %q, want it to name the program", got)
	}
}
