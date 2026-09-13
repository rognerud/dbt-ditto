package sources

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// cacheFile is the on-disk form: the provider response shape plus a header, so
// a person can read it and writing one by hand is a legitimate way to use this.
type cacheFile struct {
	Version     int       `json:"version"`
	GeneratedAt time.Time `json:"generated_at"`
	Sources     []Doc     `json:"sources"`
	Warnings    []string  `json:"warnings,omitempty"`
}

// Save writes the set to path, creating the directory if needed.
func Save(path string, set *Set) error {
	docs := make([]Doc, 0, len(set.Docs))
	for _, d := range set.Docs {
		docs = append(docs, *d)
	}
	// Sorted so a refresh that learned nothing produces an identical file, which is
	// what makes the cache reviewable in a diff.
	sort.Slice(docs, func(i, j int) bool { return docs[i].UniqueID < docs[j].UniqueID })

	body, err := json.MarshalIndent(cacheFile{
		Version:     ContractVersion,
		GeneratedAt: time.Now().UTC().Truncate(time.Second),
		Sources:     docs,
		Warnings:    set.Warnings,
	}, "", "  ")
	if err != nil {
		return err
	}
	body = append(body, '\n')

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, body, 0o644)
}

// Load reads a cache written by Save.
func Load(path string) (*Set, error) {
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return &Set{Docs: map[string]*Doc{}}, nil
	}
	if err != nil {
		return nil, err
	}

	var f cacheFile
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	if f.Version > ContractVersion {
		return nil, fmt.Errorf("%s: written by contract version %d, this build understands %d",
			path, f.Version, ContractVersion)
	}

	set := &Set{Docs: make(map[string]*Doc, len(f.Sources)), Warnings: f.Warnings}
	for i := range f.Sources {
		d := f.Sources[i]
		set.add(&d)
	}
	return set, nil
}
