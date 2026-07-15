package budget

import (
	"context"
	"sync"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

// newTestStore spins up an in-process Redis emulator (miniredis) so these
// tests run with no external services — same wire protocol and Lua
// support as real Redis, just nothing to install to try this today.
func newTestStore(t *testing.T) *Store {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { rdb.Close() })
	return New(rdb)
}

func TestReserve_AllowsUnderCap(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	res, err := s.Reserve(ctx, "key", 300_000, 1_000_000)
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	if !res.Allowed {
		t.Fatal("expected reservation to be allowed")
	}
	if res.SpentMicros != 300_000 {
		t.Fatalf("SpentMicros = %d, want 300000", res.SpentMicros)
	}
}

func TestReserve_DeniesOverCap(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if _, err := s.Reserve(ctx, "key", 900_000, 1_000_000); err != nil {
		t.Fatalf("Reserve: %v", err)
	}

	res, err := s.Reserve(ctx, "key", 200_000, 1_000_000)
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	if res.Allowed {
		t.Fatal("expected reservation to be denied")
	}
	// Denial must not mutate the running total.
	if res.SpentMicros != 900_000 {
		t.Fatalf("SpentMicros = %d, want unchanged 900000", res.SpentMicros)
	}
}

func TestReserve_ConcurrentNoDoubleSpend(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	const (
		capMicros    = 1_000_000 // $1.00 hard cap
		amountMicros = 50_000    // $0.05 per request
		requests     = 200       // far more concurrent demand than the cap allows
	)
	wantAllowed := capMicros / amountMicros // exactly 20 can fit

	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		allowed int
	)
	for i := 0; i < requests; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := s.Reserve(ctx, "concurrent-key", amountMicros, capMicros)
			if err != nil {
				t.Error(err)
				return
			}
			if res.Allowed {
				mu.Lock()
				allowed++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	if allowed != int(wantAllowed) {
		t.Fatalf("got %d allowed reservations under concurrent load, want exactly %d (double-spend or lost reservation)", allowed, wantAllowed)
	}
}

// BenchmarkReserve demonstrates that a single Reserve call costs one fixed
// Redis round trip regardless of how many prior reservations have already
// landed on the key — the check doesn't get slower as spend history grows.
func BenchmarkReserve(b *testing.B) {
	mr, err := miniredis.Run()
	if err != nil {
		b.Fatal(err)
	}
	defer mr.Close()
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()
	s := New(rdb)
	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := s.Reserve(ctx, "bench-key", 1, 1<<62); err != nil {
			b.Fatal(err)
		}
	}
}
