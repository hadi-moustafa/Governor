package budget_test

import (
	"context"
	"fmt"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/hadi-moustafa/governor/internal/budget"
)

// Example simulates a gateway's pre-flight check on a sequence of
// requests against a $1.00 hard cap, using the default MemoryStore — the
// backend a single Governor process uses with zero external
// infrastructure. The fourth request would push spend to $1.20, so it's
// denied before ever reaching a provider — no tokens bought, no latency
// added to the requests ahead of it.
func Example() {
	store := budget.NewMemoryStore()
	runDemo(store)

	// Output:
	// req 1: reserve $0.30 -> ALLOWED (running total $0.30)
	// req 2: reserve $0.30 -> ALLOWED (running total $0.60)
	// req 3: reserve $0.30 -> ALLOWED (running total $0.90)
	// req 4: reserve $0.30 -> DENIED before reaching the provider (would total $1.20, cap $1.00)
	// req 5: reserve $0.05 -> ALLOWED (running total $0.95)
}

// Example_multiInstance shows the same sequence against RedisStore. It's
// only needed once you're running more than one Governor process that
// must agree on a single shared cap — MemoryStore covers everything else.
// The behavior is identical because both satisfy budget.Store.
func Example_multiInstance() {
	mr, err := miniredis.Run()
	if err != nil {
		fmt.Println(err)
		return
	}
	defer mr.Close()

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()

	store := budget.NewRedisStore(rdb)
	runDemo(store)

	// Output:
	// req 1: reserve $0.30 -> ALLOWED (running total $0.30)
	// req 2: reserve $0.30 -> ALLOWED (running total $0.60)
	// req 3: reserve $0.30 -> ALLOWED (running total $0.90)
	// req 4: reserve $0.30 -> DENIED before reaching the provider (would total $1.20, cap $1.00)
	// req 5: reserve $0.05 -> ALLOWED (running total $0.95)
}

func runDemo(store budget.Store) {
	ctx := context.Background()
	const capMicros = 1_000_000 // $1.00
	costsMicros := []int64{300_000, 300_000, 300_000, 300_000, 50_000}

	for i, cost := range costsMicros {
		res, err := store.Reserve(ctx, "demo-key", cost, capMicros)
		if err != nil {
			fmt.Println(err)
			return
		}
		if !res.Allowed {
			fmt.Printf("req %d: reserve $%.2f -> DENIED before reaching the provider (would total $%.2f, cap $%.2f)\n",
				i+1, dollars(cost), dollars(res.SpentMicros+cost), dollars(capMicros))
			continue
		}
		fmt.Printf("req %d: reserve $%.2f -> ALLOWED (running total $%.2f)\n", i+1, dollars(cost), dollars(res.SpentMicros))
	}
}

func dollars(micros int64) float64 {
	return float64(micros) / 1_000_000
}
