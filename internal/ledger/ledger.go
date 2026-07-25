// Package ledger persists durable, reconciled cost records (Postgres) as
// the source of truth once a stream's actual usage is known.
//
// Two implementations satisfy Store, mirroring internal/budget's split:
// MemoryStore is the default — a single Governor process needs nothing
// more than an in-memory slice to record what actually happened — and
// PostgresStore (see postgres.go) is opt-in, for durability across
// restarts and visibility outside the process.
package ledger

import (
	"context"
	"time"
)

// Record is one durable, reconciled cost record for a single stream: what
// Governor estimated it would cost as chunks arrived, versus what the
// stream actually cost once it finished or was cut off.
type Record struct {
	// Key is the budget reservation key the stream was metered against
	// (see internal/proxy's reservationKey).
	Key string

	// EstimatedMicros is the sum of every Store.Reserve amount made
	// live during the stream (preflight plus each accepted chunk).
	EstimatedMicros int64

	// ActualMicros is the true cost once the stream's real usage is
	// known. For a provider that reports usage per chunk, this equals
	// EstimatedMicros; for a provider that only reports usage in a final
	// summary chunk (see internal/provider/mockfinal), it can diverge.
	ActualMicros int64

	// FinishReason is how the stream ended, e.g. "stop" or
	// "budget_exceeded".
	FinishReason string

	RecordedAt time.Time
}

// Store persists Records. Implementations don't need to be atomic the way
// budget.Store does — reconciliation runs once per stream, after the
// budget decision that mattered has already been made.
type Store interface {
	// Record durably stores r.
	Record(ctx context.Context, r Record) error
}
