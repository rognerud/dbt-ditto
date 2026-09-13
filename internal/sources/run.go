package sources

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/rognerud/dbt-ditto/internal/par"
)

// Provider is one configured external program and the sources it answers for.
type Provider struct {
	// Command is run through the platform shell, so a provider can be written as `uv run
	// ./fetch.py --project foo` without the config modelling argument splitting.
	Command string
	// Database and Schema are glob patterns limiting which sources this provider is
	// asked about. Empty matches everything.
	Database string
	Schema   string
	// Dir is the working directory the command runs in, normally where the config
	// file was found, so a relative command path means what it looks like.
	Dir string
	// Timeout bounds one invocation.
	Timeout time.Duration
}

// Claims reports whether this provider should be asked about a source.
func (p Provider) Claims(s RequestSource) bool {
	return globMatch(p.Database, s.Database) && globMatch(p.Schema, s.Schema)
}

// globMatch is an empty-means-everything wrapper over path.Match, folded,
// because warehouse identifiers are mostly not case sensitive.
func globMatch(pattern, value string) bool {
	if pattern == "" {
		return true
	}
	ok, err := path.Match(strings.ToLower(pattern), strings.ToLower(value))
	return err == nil && ok
}

// DefaultTimeout bounds a provider that has hung.
const DefaultTimeout = 2 * time.Minute

// Fetch asks every provider about the sources it claims and merges the answers.
func Fetch(ctx context.Context, providers []Provider, projects []RequestProject, want []RequestSource) *Set {
	set := &Set{Docs: map[string]*Doc{}}
	if len(providers) == 0 || len(want) == 0 {
		return set
	}

	results := make([]struct {
		resp *Response
		err  error
	}, len(providers))

	// Which sources each provider answers for, worked out before any of them run.
	claimed := make([][]RequestSource, len(providers))
	for i, p := range providers {
		for _, s := range want {
			if p.Claims(s) {
				claimed[i] = append(claimed[i], s)
			}
		}
	}
	par.Do(len(providers), func(i int) {
		if len(claimed[i]) > 0 {
			results[i].resp, results[i].err = run(ctx, providers[i], projects, claimed[i])
		}
	})

	// Merged in configured order, so "first provider to claim a source wins" is a
	// rule someone can read off their own config.
	for i, res := range results {
		p := providers[i]
		if res.err != nil {
			set.Warnings = append(set.Warnings,
				fmt.Sprintf("source provider %q: %v", p.Command, res.err))
			continue
		}
		if res.resp == nil {
			continue
		}
		set.Warnings = append(set.Warnings, res.resp.Warning...)
		for i := range res.resp.Sources {
			d := res.resp.Sources[i]
			if !set.add(&d) && d.UniqueID != "" {
				set.Warnings = append(set.Warnings, fmt.Sprintf(
					"%s: answered by more than one provider; kept the first", d.UniqueID))
			}
		}
	}
	sort.Strings(set.Warnings)
	return set
}

// run invokes one provider and decodes its answer.
func run(ctx context.Context, p Provider, projects []RequestProject, want []RequestSource) (*Response, error) {
	timeout := p.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	body, err := json.Marshal(Request{Version: ContractVersion, Projects: projects, Sources: want})
	if err != nil {
		return nil, err
	}

	sh, flag := shell()
	cmd := exec.CommandContext(ctx, sh, flag, p.Command)
	cmd.Dir = p.Dir
	cmd.Stdin = bytes.NewReader(body)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		// A provider's stderr is the only explanation a person will get.
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return nil, fmt.Errorf("%w: %s", err, lastLine(msg))
		}
		return nil, err
	}

	var resp Response
	if err := json.Unmarshal(stdout.Bytes(), &resp); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	if resp.Version > ContractVersion {
		return nil, fmt.Errorf("speaks contract version %d, this build understands %d",
			resp.Version, ContractVersion)
	}
	return &resp, nil
}

// lastLine keeps the final line of a provider's stderr: a Python traceback is
// twenty lines of noise and one line of the actual error.
func lastLine(s string) string {
	lines := strings.Split(s, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if t := strings.TrimSpace(lines[i]); t != "" {
			return t
		}
	}
	return s
}

// shell is the interpreter a provider command is handed to, and its "run this
// string" flag.
func shell() (string, string) {
	if runtime.GOOS == "windows" {
		return "cmd", "/c"
	}
	return "sh", "-c"
}
