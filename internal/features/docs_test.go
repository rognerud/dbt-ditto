package features

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cucumber/godog"
)

// The documentation whose Gherkin blocks are executed. Every ```gherkin block in
// these files is a scenario run against the real binary, so a claim in the prose
// next to one cannot quietly stop being true.
var executableDocs = []string{
	"../../docs/usage.md",
	"../../docs/configuration.md",
}

// TestDocumentationIsTrue extracts the Gherkin from the documentation and runs
// it. The markdown is the only copy: nothing is generated from a .feature file
// and nothing is checked for drift, because there is nothing to drift from.
func TestDocumentationIsTrue(t *testing.T) {
	dir := t.TempDir()
	var paths []string
	total := 0
	for _, doc := range executableDocs {
		blocks, err := gherkinBlocks(doc)
		if err != nil {
			t.Fatal(err)
		}
		if len(blocks) == 0 {
			t.Fatalf("%s carries no ```gherkin block, so nothing in it is proven", doc)
		}
		total += len(blocks)

		path := filepath.Join(dir, strings.TrimSuffix(filepath.Base(doc), ".md")+".feature")
		if err := os.WriteFile(path, []byte(feature(doc, blocks)), 0o644); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, path)
	}

	suite := godog.TestSuite{
		ScenarioInitializer: initScenario,
		Options: &godog.Options{
			Format:   "pretty",
			Paths:    paths,
			TestingT: t,
			Strict:   true,
		},
	}
	if suite.Run() != 0 {
		t.Fatalf("the documentation is not true: %d scenarios extracted from %v", total, executableDocs)
	}
}

// block is one fenced Gherkin block and the heading it was written under, which
// is what makes a failure point back at a place in the documentation.
type block struct {
	heading string
	line    int
	body    string
}

// gherkinBlocks reads the ```gherkin blocks out of a markdown file, in order,
// each tagged with the nearest heading above it.
func gherkinBlocks(path string) ([]block, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var (
		out     []block
		heading string
		current *block
	)
	for i, line := range strings.Split(string(raw), "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case current != nil && trimmed == "```":
			out = append(out, *current)
			current = nil
		case current != nil:
			current.body += line + "\n"
		case trimmed == "```gherkin":
			current = &block{heading: heading, line: i + 2}
		case strings.HasPrefix(trimmed, "#"):
			heading = strings.TrimSpace(strings.TrimLeft(trimmed, "#"))
		}
	}
	if current != nil {
		return nil, fmt.Errorf("%s: a ```gherkin block is never closed", path)
	}
	return out, nil
}

// feature assembles the blocks of one document into a feature file. A block
// holds scenarios only: the Feature line is the document, and the tag on each
// scenario is where in the document it came from, so godog's output reads as a
// list of documented promises.
func feature(doc string, blocks []block) string {
	var b strings.Builder
	name := strings.TrimPrefix(doc, "../../")
	fmt.Fprintf(&b, "Feature: %s is true\n", name)
	fmt.Fprintf(&b, "  Every Gherkin block in %s, run against the real binary.\n\n", name)
	for _, blk := range blocks {
		fmt.Fprintf(&b, "  @%s\n", anchor(name, blk.heading))
		fmt.Fprintf(&b, "  # %s:%d\n", name, blk.line)
		b.WriteString(blk.body)
		b.WriteString("\n")
	}
	return b.String()
}

// anchor renders a document heading as the markdown anchor that links to it, so
// a tag in the output is the address of the prose the scenario proves.
func anchor(doc, heading string) string {
	var b strings.Builder
	b.WriteString(doc)
	b.WriteString("#")
	for _, r := range strings.ToLower(heading) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_', r == '-':
			b.WriteRune(r)
		case r == ' ':
			b.WriteRune('-')
		}
	}
	return b.String()
}
