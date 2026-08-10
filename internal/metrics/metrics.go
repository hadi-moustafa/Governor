// Package metrics tracks basic, in-process counters on the events that
// matter most for a spend-control gateway: how often a request gets
// rejected before it ever reaches a provider, how often a stream gets cut
// off mid-flight, and how much estimated-vs-actual cost drift
// reconciliation finds. It's deliberately not a Prometheus/OpenTelemetry
// client — no new dependency, just atomic counters and a JSON snapshot —
// since nothing in this project yet needs a real metrics backend to scrape
// from; Counters.Handler is enough to point curl or a debug dashboard at.
package metrics

import "sync/atomic"

// Counters are safe for concurrent use — every field is updated with a
// single atomic add, the same reasoning budget.MemoryStore's mutex-guarded
// counter relies on, just lock-free since these never need a
// check-then-act critical section the way a spend cap does.
type Counters struct {
	// PreflightDenials counts requests rejected before ever reaching a
	// provider (budget.Store.Reserve denied the preflight reservation).
	PreflightDenials atomic.Int64
	// MidStreamCutoffs counts streams whose upstream request was
	// canceled after a per-chunk reservation was denied.
	MidStreamCutoffs atomic.Int64
	// StreamsCompleted counts streams that ended normally (EOF).
	StreamsCompleted atomic.Int64
	// StreamsErrored counts streams that ended due to a provider or
	// transport error (not a budget denial).
	StreamsErrored atomic.Int64
	// Reconciliations counts every ledger.Record written.
	Reconciliations atomic.Int64
	// RefundsIssued counts reconciliations that found an over-reservation
	// and refunded it (see proxy.Handler.reconcile).
	RefundsIssued atomic.Int64
	// DriftMicrosTotal accumulates every refunded amount, in micros —
	// the running total of speculative reservation that bought no real
	// content.
	DriftMicrosTotal atomic.Int64
}

// Snapshot is a point-in-time, non-atomic copy of Counters, suitable for
// encoding (e.g. to JSON) or asserting on in a test.
type Snapshot struct {
	PreflightDenials int64 `json:"preflight_denials"`
	MidStreamCutoffs int64 `json:"mid_stream_cutoffs"`
	StreamsCompleted int64 `json:"streams_completed"`
	StreamsErrored   int64 `json:"streams_errored"`
	Reconciliations  int64 `json:"reconciliations"`
	RefundsIssued    int64 `json:"refunds_issued"`
	DriftMicrosTotal int64 `json:"drift_micros_total"`
}

// Snapshot reads every counter's current value. Individual fields may be
// updated concurrently with (or between) reads — this is a best-effort
// snapshot, not a consistent multi-field transaction, which is fine for
// observability but not something to build correctness logic on top of.
func (c *Counters) Snapshot() Snapshot {
	return Snapshot{
		PreflightDenials: c.PreflightDenials.Load(),
		MidStreamCutoffs: c.MidStreamCutoffs.Load(),
		StreamsCompleted: c.StreamsCompleted.Load(),
		StreamsErrored:   c.StreamsErrored.Load(),
		Reconciliations:  c.Reconciliations.Load(),
		RefundsIssued:    c.RefundsIssued.Load(),
		DriftMicrosTotal: c.DriftMicrosTotal.Load(),
	}
}
