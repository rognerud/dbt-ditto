// Package sources documents external sources — the raw tables no dbt project builds,
// which inheritance can never reach — by writing the sources it wants answers for to an
// external program's stdin and reading documentation back.
package sources

// ContractVersion is the wire format both sides agree on.
const ContractVersion = 1

// Request is the JSON written to a provider's stdin.
type Request struct {
	Version  int              `json:"version"`
	Projects []RequestProject `json:"projects"`
	Sources  []RequestSource  `json:"sources"`
}

// RequestProject describes the dbt project a source belongs to, so a provider
// authenticates the way dbt does rather than keeping a second copy of the credentials.
type RequestProject struct {
	Name        string `json:"name"`
	Root        string `json:"root"`
	Profile     string `json:"profile"`
	Target      string `json:"target"`
	ProfilesDir string `json:"profiles_dir"`
}

// RequestSource identifies one relation to describe.
type RequestSource struct {
	UniqueID   string `json:"unique_id"`
	Database   string `json:"database"`
	Schema     string `json:"schema"`
	Identifier string `json:"identifier"`
	// SourceName and Name are the dbt-side names, for a provider that wants to log
	// something a person will recognise.
	SourceName string `json:"source_name"`
	Name       string `json:"name"`
	// Project names the entry in Request.Projects holding the connection configuration.
	Project string `json:"project"`
}

// Response is the JSON read from a provider's stdout.
type Response struct {
	Version int      `json:"version"`
	Sources []Doc    `json:"sources"`
	Warning []string `json:"warnings"`
}

// Doc is what a provider found out about one relation.
type Doc struct {
	UniqueID    string            `json:"unique_id"`
	Description string            `json:"description"`
	Meta        map[string]any    `json:"meta"`
	Tags        []string          `json:"tags"`
	Labels      map[string]string `json:"labels"`
	Columns     []ColumnDoc       `json:"columns"`
}

// ColumnDoc is one column of a relation.
type ColumnDoc struct {
	Name        string            `json:"name"`
	DataType    string            `json:"data_type"`
	Description string            `json:"description"`
	Meta        map[string]any    `json:"meta"`
	Tags        []string          `json:"tags"`
	Labels      map[string]string `json:"labels"`
	Extra       map[string]any    `json:"extra"`
	// Index is the column's ordinal, used by catalog ordering.
	Index int `json:"index"`
}

// Set is the merged answer from every provider, keyed by unique_id.
type Set struct {
	Docs map[string]*Doc
	// Warnings are what the providers said went wrong, plus anything the merging noticed.
	Warnings []string
}

// add files one relation's documentation, reporting whether it was the first
// answer for that node: later ones, from another provider or a stale cache
// entry, are kept out.
func (s *Set) add(d *Doc) bool {
	if d.UniqueID == "" {
		return false
	}
	if _, taken := s.Docs[d.UniqueID]; taken {
		return false
	}
	s.Docs[d.UniqueID] = d
	return true
}

// Lookup returns the documentation for a node, if any provider had some.
func (s *Set) Lookup(uniqueID string) (*Doc, bool) {
	if s == nil || len(s.Docs) == 0 {
		return nil, false
	}
	d, ok := s.Docs[uniqueID]
	return d, ok
}

// Len reports how many relations were documented.
func (s *Set) Len() int {
	if s == nil {
		return 0
	}
	return len(s.Docs)
}
