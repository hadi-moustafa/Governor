// Package budget implements atomic reserve/decrement of per-key spend
// caps and pre-flight cost checks.
//
// Two backends satisfy the same Store interface. MemoryStore is the
// default: a single process enforcing its own cap needs nothing more than
// a mutex-guarded counter, so a solo dev running Governor as one binary
// pulls in zero external infrastructure. RedisStore exists for the case
// MemoryStore can't cover — multiple gateway processes (replicas, a
// restart-durable cap) that all need to agree on one running total —
// and is opt-in, not required to run Governor at all.
package budget

import "context"

// Result is the outcome of a Reserve call.
type Result struct {
	// Allowed reports whether the reservation was committed.
	Allowed bool
	// SpentMicros is the running total for the key after this call if
	// Allowed, or the running total before it (unchanged) if denied.
	SpentMicros int64
}

// Store enforces a per-key spend cap. Implementations must make Reserve
// atomic: under concurrent calls racing for the last bit of budget, the
// number allowed through must never exceed what the cap permits.
type Store interface {
	// Reserve atomically reserves amountMicros against key's running
	// total, refusing if doing so would push the total past capMicros.
	// Callers must check Result.Allowed before forwarding a request
	// upstream — a denied reservation must never reach a provider, since
	// paying for it anyway would defeat the point of a hard cap.
	Reserve(ctx context.Context, key string, amountMicros, capMicros int64) (Result, error)

	// Refund reduces key's running total by deltaMicros. Used to true up
	// an over-estimated reservation once actual cost is known — e.g.
	// after a mid-stream cutoff, or during ledger reconciliation once a
	// stream finishes and real token usage is in.
	Refund(ctx context.Context, key string, deltaMicros int64) error
}
