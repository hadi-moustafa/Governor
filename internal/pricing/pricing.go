// Package pricing converts token counts into cost. This is a deliberately
// trivial placeholder — a single flat rate, no prompt/completion split, no
// per-model variance — standing in for a real versioned per-model/per-token
// pricing table, which is out of scope for proving mid-stream cutoff.
package pricing

// Model prices tokens at a single flat rate.
type Model struct {
	// MicrosPerToken is the cost of one token, in micros (1 unit = $0.000001).
	MicrosPerToken int64
}

// CostMicros returns the cost of tokens tokens, in micros.
func (m Model) CostMicros(tokens int) int64 {
	return int64(tokens) * m.MicrosPerToken
}
