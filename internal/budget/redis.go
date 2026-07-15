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

// RedisStore is the multi-process Store: use it when more than one
// Governor instance needs to agree on the same running total, or when the
// cap needs to survive a process restart. A single-process deployment
// doesn't need this — see MemoryStore, the default.
type RedisStore struct {
	rdb *redis.Client
}

// NewRedisStore wraps an existing Redis client. The caller owns the
// client's lifecycle (including Close).
func NewRedisStore(rdb *redis.Client) *RedisStore {
	return &RedisStore{rdb: rdb}
}

var _ Store = (*RedisStore)(nil)

// Reserve implements Store.
func (s *RedisStore) Reserve(ctx context.Context, key string, amountMicros, capMicros int64) (Result, error) {
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

// Refund implements Store.
func (s *RedisStore) Refund(ctx context.Context, key string, deltaMicros int64) error {
	if deltaMicros < 0 {
		return errors.New("budget: deltaMicros must be >= 0")
	}
	if deltaMicros == 0 {
		return nil
	}
	return s.rdb.DecrBy(ctx, key, deltaMicros).Err()
}
