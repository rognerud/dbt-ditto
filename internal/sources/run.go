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
	"sync"
	"time"
)

// Provider is one configured external program and the sources it answers for.
type Provider struct {
	// Command is run through the platform shell, so a provider can be written
	// as `uv run ./fetch.py --project foo` without the config having to model
	// argument splitting. Providers are named in a project's own config file,
	// by the people who run the tool, so this is no more powerful than what
	// they can already do.
	Command string
	// Database and Schema are glob patterns limiting which sources this
	// provider is asked about. Empty matches everything.
	Database string
	Schema   string
	// Dir is the working directory the command runs in, normally the directory
	// the config file was found in, so a relative command path means what a
	// person reading the config thinks it means.
	Dir string
	// Timeout bounds one invocation.
	Timeout time.Duration
}

// Claims reports whether this provider should be asked about a source.
func (p Provider) Claims(s RequestSource) bool {
	return globMatch(p.Database, s.Database) && globMatch(p.Schema, s.Schema)
}

// globMatch is an empty-means-everything wrapper over path.Match, folded,
// because warehouse identifiers are not case sensitive in most places and a
// pattern that matches only one spelling is a trap.
func globMatch(pattern, value string) bool {
	if pattern == "" {
		return true
	}
	ok, err := path.Match(strings.ToLower(pattern), strings.ToLower(value))
	return err == nil && ok
}

// DefaultTimeout bounds a provider that has hung. Metadata lookups are fast;
// anything past this is a provider waiting on something it will not get, and a
// documentation run should not be held open by it.
const DefaultTimeout = 2 * time.Minute

// Fetch asks every provider about the sources it claims and merges the answers.
//
// Providers run in parallel and independently: one failing produces a warning
// and the others still contribute, because a tool that formats YAML should not
// stop doing it because BigQuery is unreachable. Call Fetch only from an
// explicit refresh — the ordinary run reads the cache, so `--check` in CI never
// spawns anything and needs no credentials.
func Fetch(ctx context.Context, providers []Provider, projects []RequestProject, want []RequestSource) *Set {
	set := &Set{Docs: map[string]*Doc{}}
	if len(providers) == 0 || len(want) == 0 {
		return set
	}

	type result struct {
		provider int
		resp     *Response
		err      error
		claimed  int
	}
	results := make([]result, len(providers))

	var wg sync.WaitGroup
	sem := make(chan struct{}, runtime.NumCPU())
	for i, p := range providers {
		claimed := make([]RequestSource, 0, len(want))
		for _, s := range want {
			if p.Claims(s) {
				claimed = append(claimed, s)
			}
		}
		results[i] = result{provider: i, claimed: len(claimed)}
		if len(claimed) == 0 {
			continue
		}
		wg.Add(1)
		go func(i int, p Provider, claimed []RequestSource) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			resp, err := run(ctx, p, projects, claimed)
			results[i].resp, results[i].err = resp, err
		}(i, p, claimed)
	}
	wg.Wait()

	// Merged in configured order so "first provider to claim a source wins" is
	// a rule someone can read off their own config.
	for _, res := range results {
		p := providers[res.provider]
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
			if d.UniqueID == "" {
				continue
			}
			if _, taken := set.Docs[d.UniqueID]; taken {
				set.Warnings = append(set.Warnings, fmt.Sprintf(
					"%s: answered by more than one provider; kept the first", d.UniqueID))
				continue
			}
			set.Docs[d.UniqueID] = &d
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

	cmd := exec.CommandContext(ctx, shell(), shellFlag(), p.Command)
	cmd.Dir = p.Dir
	cmd.Stdin = bytes.NewReader(body)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		// A provider's stderr is the only explanation a person will get, so it
		// is carried into the warning rather than discarded with the exit code.
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

// lastLine keeps the final line of a provider's stderr. A Python traceback is
// twenty lines of this tool's users' business and one line of the actual error.
func lastLine(s string) string {
	lines := strings.Split(s, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if t := strings.TrimSpace(lines[i]); t != "" {
			return t
		}
	}
	return s
}

func shell() string {
	if runtime.GOOS == "windows" {
		return "cmd"
	}
	return "sh"
}

func shellFlag() string {
	if runtime.GOOS == "windows" {
		return "/c"
	}
	return "-c"
}
