// Package sources documents external sources — the raw tables no dbt project
// builds, which inheritance can never reach — by asking an external program
// about them: dbt-ditto writes the sources it wants answers for to a provider's
// stdin and reads documentation back from its stdout.
//
// See docs/source-providers.md for the design and the reasoning.
package sources

// ContractVersion is the wire format both sides agree on. Unknown fields are
// ignored in both directions, so the contract can grow without a bump; this
// moves only for a change that would make an old provider wrong rather than
// merely incomplete.
const ContractVersion = 1

// Request is the JSON written to a provider's stdin.
type Request struct {
	Version  int              `json:"version"`
	Projects []RequestProject `json:"projects"`
	Sources  []RequestSource  `json:"sources"`
}

// RequestProject describes the dbt project a source belongs to, so a provider
// authenticates the way dbt already does rather than keeping a second copy of
// the credentials. Root, Profile and Target are enough to resolve a connection
// through `dbt.config.profile.Profile.render_from_args`, or failing that through
// profiles.yml read directly. ProfilesDir follows dbt's own resolution:
// DBT_PROFILES_DIR, else the project directory, else ~/.dbt.
type RequestProject struct {
	Name        string `json:"name"`
	Root        string `json:"root"`
	Profile     string `json:"profile"`
	Target      string `json:"target"`
	ProfilesDir string `json:"profiles_dir"`
}

// RequestSource identifies one relation to describe. The unique_id is what the
// answer is matched back on; the three relation parts are what the provider
// actually looks up, since no warehouse has heard of a dbt unique_id.
type RequestSource struct {
	UniqueID   string `json:"unique_id"`
	Database   string `json:"database"`
	Schema     string `json:"schema"`
	Identifier string `json:"identifier"`
	// SourceName and Name are the dbt-side names, passed through for a provider
	// that wants to log something a person will recognise.
	SourceName string `json:"source_name"`
	Name       string `json:"name"`
	// Project names the entry in Request.Projects holding the connection
	// configuration for this relation. A run spanning two projects can span two
	// warehouses, so this is per source rather than per request.
	Project string `json:"project"`
}

// Response is the JSON read from a provider's stdout.
type Response struct {
	Version int      `json:"version"`
	Sources []Doc    `json:"sources"`
	Warning []string `json:"warnings"`
}

// Doc is what a provider found out about one relation.
//
// Description, Meta and Tags are dbt's own vocabulary, for a provider stating
// something outright. Labels is the neutral bucket: key-value pairs attached to
// the object, which is the one piece of metadata every warehouse has and the
// only one dbt-ditto routes itself. Extra carries anything else the column
// should end up with — a BigQuery policy tag, say — under the key it is written
// with in dbt YAML.
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
	// Index is the column's ordinal in the relation, used by catalog ordering.
	// Zero for every column means "no opinion", and the listed order is used.
	Index int `json:"index"`
}

// Set is the merged answer from every provider, keyed by unique_id.
type Set struct {
	Docs map[string]*Doc
	// Warnings are what the providers said went wrong, plus anything the
	// merging noticed. They never fail a run on their own.
	Warnings []string
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
