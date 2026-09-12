package runner_test

import (
	"testing"

	"github.com/rognerud/dbt-ditto/internal/config"
)

// The propagation settings are about what happens to a label as a column
// changes shape on its way downstream, so each needs a model that reads the
// external source and renames its column in one particular way.

// stageWithPackedSourceColumn puts a model below the source whose column is the
// same data in a struct: `order.order_id` packed from `order_id`. Nothing about
// the value changed, so a classification true of one is true of the other.
func stageWithPackedSourceColumn(t *testing.T) (string, *config.Config) {
	t.Helper()
	s := newStage()
	s.nodes["model.demo.packed"] = modelNode("packed", "models/_packed.yml",
		[]string{externalSource},
		column("order.order_id", ""),
	)
	s.files["models/_packed.yml"] = "version: 2\nmodels:\n  - name: packed\n    columns:\n" +
		"      - name: order.order_id\n"

	dir := s.write(t, t.TempDir())
	writeSourceCache(t, dir, sourceDoc(labelledColumn()))
	return dir, stageConfig(dir)
}

// stageWithAggregatedSourceColumn puts a model below the source whose column is
// an aggregate: `total_order_id`. The description still applies; the value the
// label classified does not survive being summed.
func stageWithAggregatedSourceColumn(t *testing.T) (string, *config.Config) {
	t.Helper()
	s := newStage()
	s.nodes["model.demo.agg"] = modelNode("agg", "models/_agg.yml",
		[]string{externalSource},
		column("total_order_id", ""),
	)
	s.files["models/_agg.yml"] = "version: 2\nmodels:\n  - name: agg\n    columns:\n" +
		"      - name: total_order_id\n"

	dir := s.write(t, t.TempDir())
	writeSourceCache(t, dir, sourceDoc(labelledColumn()))
	return dir, stageConfig(dir)
}

// stageWithConflictingLabels puts two sources in one generation that label the
// same column differently, which is the disagreement `on_conflict` is about.
//
// Both are external, so both get their labels from a provider; the model below
// reads both, so the two arrive in the same generation and the winner would
// otherwise be whichever unique_id sorts first.
func stageWithConflictingLabels(t *testing.T) (string, *config.Config) {
	t.Helper()
	s := newStage()
	s.sources["source.demo.billing.orders"] = sourceNode("billing", "orders_billing", "models/_sources.yml",
		column("order_id", ""),
	)
	s.nodes["model.demo.both"] = modelNode("both", "models/_both.yml",
		[]string{externalSource, "source.demo.billing.orders"},
		column("order_id", ""),
	)
	s.files["models/_both.yml"] = "version: 2\nmodels:\n  - name: both\n    columns:\n" +
		"      - name: order_id\n"

	dir := s.write(t, t.TempDir())
	writeSourceCache(t, dir,
		sourceDoc(labelledColumn()),
		map[string]any{
			"unique_id": "source.demo.billing.orders",
			"columns": []map[string]any{
				sourceColumn("order_id", withLabels(map[string]string{"classification": "public"})),
			},
		},
	)
	return dir, stageConfig(dir)
}

// providerStage is the shared stage with one provider configured. Providers are
// only ever spawned by a refresh, so every case using this sets refresh.
func providerStage(t *testing.T, command string) (string, *config.Config) {
	t.Helper()
	dir := newStage().write(t, t.TempDir())
	cfg := stageConfig(dir)
	cfg.Sources.Providers = []config.SourceProvider{{Command: command}}
	return dir, cfg
}
