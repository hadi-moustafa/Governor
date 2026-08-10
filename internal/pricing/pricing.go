// Package pricing converts token counts into cost. Two implementations
// exist: Model is a single flat rate with no per-model variance — good
// enough (and still used by most tests, for predictable arithmetic) for
// proving mid-stream cutoff, which never depended on real prices. Table
// (see table.go) is the real thing: versioned per-model rates, for
// pricing real provider traffic accurately.
package pricing

// Pricer converts a token count for a given model into cost, in micros
// (1 unit = $0.000001). Both Model and Table satisfy this, so
// proxy.Handler can take either without knowing which it got.
type Pricer interface {
	CostMicros(model string, tokens int) int64
}

// Model prices every model's tokens at the same flat rate.
type Model struct {
	// MicrosPerToken is the cost of one token, in micros (1 unit = $0.000001).
	MicrosPerToken int64
}

var _ Pricer = Model{}

// CostMicros returns the cost of tokens tokens, in micros. model is
// accepted to satisfy Pricer but ignored — every model costs the same
// under Model.
func (m Model) CostMicros(_ string, tokens int) int64 {
	return int64(tokens) * m.MicrosPerToken
}
