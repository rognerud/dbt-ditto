package config

// normalise folds a description down to the form placeholders are compared in.
// dbt-osmosis compares placeholders with a plain `in` against the raw
// description, so the comparison is exact: no trimming, no case folding.
func normalise(s string) string { return s }

// IsPlaceholder reports whether a description counts as undocumented and may
// therefore be overwritten by an inherited one.
func (r Resolved) IsPlaceholder(desc string) bool {
	return r.Placeholders[normalise(desc)]
}
