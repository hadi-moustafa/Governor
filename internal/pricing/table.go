package pricing

// Rates is one model's per-token price.
type Rates struct {
	// MicrosPerToken is the cost of one token, in micros (1 unit =
	// $0.000001). This prices output tokens only: Governor's live
	// per-chunk metering (see internal/proxy/stream.go) measures
	// generation as it streams, and neither provider adapter currently
	// splits input vs. output tokens per chunk (OpenAI's only reports a
	// combined total on its final chunk; Anthropic's message_delta
	// reports output tokens incrementally, with input tokens arriving
	// separately in message_start, which nothing here reads yet — see
	// internal/provider/openai and internal/provider/anthropic). Real
	// input-token cost isn't priced by this table at all yet; it's only
	// covered by the flat PreflightTokens estimate reserved before a
	// request is sent.
	MicrosPerToken int64
}

// Table prices tokens per-model, falling back to Fallback for any model
// not in Rates. Use NewTable rather than constructing one directly.
type Table struct {
	rates    map[string]Rates
	fallback Rates
}

// NewTable returns a Table pricing each model in rates individually,
// falling back to fallback for anything else — e.g. a new model release
// this table hasn't been updated for yet.
func NewTable(rates map[string]Rates, fallback Rates) Table {
	return Table{rates: rates, fallback: fallback}
}

var _ Pricer = Table{}

// CostMicros returns the cost of tokens tokens for model, in micros,
// using model's own rate if known or Fallback otherwise.
func (t Table) CostMicros(model string, tokens int) int64 {
	r, ok := t.rates[model]
	if !ok {
		r = t.fallback
	}
	return int64(tokens) * r.MicrosPerToken
}

// Lookup returns model's rate and whether it was found (as opposed to
// Fallback being used).
func (t Table) Lookup(model string) (Rates, bool) {
	r, ok := t.rates[model]
	return r, ok
}

// Snapshot20260810 is a versioned, dated snapshot of illustrative
// per-output-token rates for a handful of well-known OpenAI and
// Anthropic models. These are approximate, rounded, point-in-time
// figures for exercising Table — provider prices change, and this
// snapshot is not re-fetched automatically (there's no live pricing
// API to check against). Verify against each provider's current
// published pricing page before relying on this for real billing, and
// add a new dated Snapshot rather than mutating this one when rates
// change, so old deployments pinned to a snapshot keep working.
var Snapshot20260810 = NewTable(
	map[string]Rates{
		"gpt-4o":                     {MicrosPerToken: 10_000}, // ~$10 / 1M output tokens
		"gpt-4o-mini":                {MicrosPerToken: 600},    // ~$0.60 / 1M output tokens
		"claude-3-5-sonnet-20241022": {MicrosPerToken: 15_000}, // ~$15 / 1M output tokens
		"claude-3-5-haiku-20241022":  {MicrosPerToken: 4_000},  // ~$4 / 1M output tokens
	},
	Rates{MicrosPerToken: 10_000}, // fallback: price an unrecognized model like gpt-4o rather than free
)
