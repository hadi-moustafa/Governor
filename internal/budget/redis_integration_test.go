package budget_test

import (
	"context"
	"os"
	"testing"

	"github.com/redis/go-redis/v9"

	"github.com/hadi-moustafa/governor/internal/budget"
)

// TestRedisStore_AgainstRealRedis re-runs the core correctness proof from
// budget_test.go — the one that actually matters for RedisStore, since its
// atomicity guarantee comes from Redis serializing Lua script execution,
// not from anything miniredis-specific — against a real Redis instance
// instead of the in-process emulator every other test in this package uses.
// miniredis implements the same wire protocol and Lua support, but this is
// the only test that proves RedisStore works against the real thing.
//
// Skipped unless GOVERNOR_TEST_REDIS_ADDR is set, so `go test ./...` never
// requires Docker/Redis to be running. Point it at the redis service in
// the repo's docker-compose.yml, e.g.:
//
//	GOVERNOR_TEST_REDIS_ADDR=localhost:6379 go test ./internal/budget/...
func TestRedisStore_AgainstRealRedis(t *testing.T) {
	addr := os.Getenv("GOVERNOR_TEST_REDIS_ADDR")
	if addr == "" {
		t.Skip("GOVERNOR_TEST_REDIS_ADDR not set — skipping real-Redis integration test (see docker-compose.yml)")
	}

	rdb := redis.NewClient(&redis.Options{Addr: addr})
	defer rdb.Close()

	ctx := context.Background()
	if err := rdb.Ping(ctx).Err(); err != nil {
		t.Fatalf("could not reach Redis at %s: %v", addr, err)
	}

	// Use a fresh key per run so repeated test runs against the same
	// long-lived container don't interfere with each other.
	key := "governor-test:" + t.Name()
	t.Cleanup(func() { rdb.Del(context.Background(), key) })

	store := budget.NewRedisStore(rdb)

	const (
		capMicros    = 1_000_000 // $1.00 hard cap
		amountMicros = 50_000    // $0.05 per request
		requests     = 200       // far more concurrent demand than the cap allows
	)
	wantAllowed := capMicros / amountMicros // exactly 20 can fit

	var (
		allowedCh = make(chan bool, requests)
	)
	for i := 0; i < requests; i++ {
		go func() {
			res, err := store.Reserve(ctx, key, amountMicros, capMicros)
			if err != nil {
				t.Error(err)
				allowedCh <- false
				return
			}
			allowedCh <- res.Allowed
		}()
	}

	allowed := 0
	for i := 0; i < requests; i++ {
		if <-allowedCh {
			allowed++
		}
	}

	if allowed != wantAllowed {
		t.Fatalf("got %d allowed reservations against real Redis, want exactly %d (double-spend or lost reservation)", allowed, wantAllowed)
	}
}
