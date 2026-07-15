// Package budget implements atomic reserve/decrement of spend caps
// (backed by Redis) and pre-flight cost checks.
package budget

import (
	"context"
	"errors"
	"fmt"

	"github.com/redis/go-redis/v9"
)

// reserveScript is the pre-flight check. It runs as a single Lua script
// inside Redis, so the "read the running total, decide, write the new
// total" sequence happens as one atomic server-side step instead of
// three separate client round trips. That's what makes it safe under
// concurrent requests without a client-side lock: Redis serializes script
// execution, so two requests racing for the last bit of budget can never
// both read the same "current" value and both be allowed through.
//
// It also answers the latency question: the cost of the check is one
// fixed-size Redis command (GET + conditional INCRBY on an integer
// counter) — O(1) in the number of prior requests, not a scan over
// history. A request made after ten thousand prior calls costs exactly
// the same pre-flight round trip as the first one.
const reserveScript = `
local current = tonumber(redis.call('GET', KEYS[1]) or '0')
local amount = tonumber(ARGV[1])
local cap = tonumber(ARGV[2])
if current + amount > cap then
  return {0, current}
end
local newTotal = redis.call('INCRBY', KEYS[1], amount)
return {1, newTotal}
`

// Store enforces per-key spend caps against Redis. All amounts are in
// micros (1 unit = $0.000001) so budgets never touch floating point.
type Store struct {
	rdb *redis.Client
}

// New wraps an existing Redis client. The caller owns the client's
// lifecycle (including Close).
func New(rdb *redis.Client) *Store {
	return &Store{rdb: rdb}
}

// Result is the outcome of a Reserve call.
type Result struct {
	// Allowed reports whether the reservation was committed.
	Allowed bool
	// SpentMicros is the running total for the key after this call if
	// Allowed, or the running total before it (unchanged) if denied.
	SpentMicros int64
}

// Reserve atomically reserves amountMicros against key's running total,
// refusing if doing so would push the total past capMicros. Callers must
// check Result.Allowed before forwarding a request upstream — a denied
// reservation must never reach a provider, since paying for it anyway
// would defeat the point of a hard cap.
func (s *Store) Reserve(ctx context.Context, key string, amountMicros, capMicros int64) (Result, error) {
	if amountMicros < 0 {
		return Result{}, errors.New("budget: amountMicros must be >= 0")
	}
	raw, err := s.rdb.Eval(ctx, reserveScript, []string{key}, amountMicros, capMicros).Result()
	if err != nil {
		return Result{}, fmt.Errorf("budget: reserve: %w", err)
	}
	vals, ok := raw.([]interface{})
	if !ok || len(vals) != 2 {
		return Result{}, fmt.Errorf("budget: unexpected script reply: %v", raw)
	}
	allowed, _ := vals[0].(int64)
	total, _ := vals[1].(int64)
	return Result{Allowed: allowed == 1, SpentMicros: total}, nil
}

// Refund reduces key's running total by deltaMicros. Used to true up an
// over-estimated reservation once actual cost is known — e.g. after a
// mid-stream cutoff, or during ledger reconciliation once a stream
// finishes and real token usage is in.
func (s *Store) Refund(ctx context.Context, key string, deltaMicros int64) error {
	if deltaMicros < 0 {
		return errors.New("budget: deltaMicros must be >= 0")
	}
	if deltaMicros == 0 {
		return nil
	}
	return s.rdb.DecrBy(ctx, key, deltaMicros).Err()
}
