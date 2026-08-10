package pricing_test

import (
	"testing"

	"github.com/hadi-moustafa/governor/internal/pricing"
)

func TestTable_PricesKnownModelByItsOwnRate(t *testing.T) {
	table := pricing.NewTable(
		map[string]pricing.Rates{
			"model-a": {MicrosPerToken: 100},
			"model-b": {MicrosPerToken: 5},
		},
		pricing.Rates{MicrosPerToken: 1},
	)

	if got := table.CostMicros("model-a", 10); got != 1000 {
		t.Errorf("CostMicros(model-a, 10) = %d, want 1000", got)
	}
	if got := table.CostMicros("model-b", 10); got != 50 {
		t.Errorf("CostMicros(model-b, 10) = %d, want 50", got)
	}
}

func TestTable_FallsBackForUnknownModel(t *testing.T) {
	table := pricing.NewTable(
		map[string]pricing.Rates{"model-a": {MicrosPerToken: 100}},
		pricing.Rates{MicrosPerToken: 7},
	)

	if got := table.CostMicros("some-future-model", 10); got != 70 {
		t.Errorf("CostMicros(some-future-model, 10) = %d, want 70 (fallback rate)", got)
	}
}

func TestTable_LookupReportsWhetherModelWasFound(t *testing.T) {
	table := pricing.NewTable(
		map[string]pricing.Rates{"model-a": {MicrosPerToken: 100}},
		pricing.Rates{MicrosPerToken: 7},
	)

	if _, ok := table.Lookup("model-a"); !ok {
		t.Error("Lookup(model-a) ok = false, want true")
	}
	if _, ok := table.Lookup("unknown"); ok {
		t.Error("Lookup(unknown) ok = true, want false")
	}
}

func TestSnapshot20260810_HasEntriesForBothProviders(t *testing.T) {
	for _, model := range []string{"gpt-4o", "gpt-4o-mini", "claude-3-5-sonnet-20241022", "claude-3-5-haiku-20241022"} {
		if _, ok := pricing.Snapshot20260810.Lookup(model); !ok {
			t.Errorf("Snapshot20260810 has no entry for %q", model)
		}
	}
}

func TestModel_SatisfiesPricerAndIgnoresModelName(t *testing.T) {
	var p pricing.Pricer = pricing.Model{MicrosPerToken: 50}
	if got := p.CostMicros("anything", 3); got != 150 {
		t.Errorf("CostMicros(anything, 3) = %d, want 150", got)
	}
	if got := p.CostMicros("something-else", 3); got != 150 {
		t.Errorf("CostMicros(something-else, 3) = %d, want 150 (Model must ignore model name)", got)
	}
}
