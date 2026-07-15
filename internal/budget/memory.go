package budget

import (
	"context"
	"errors"
	"sync"
)

// MemoryStore is an in-process Store: a mutex-guarded counter per key.
// This is the default backend — it's what a single Governor process
// needs to enforce its own cap correctly, with no external service to
// run. Amounts are in micros (1 unit = $0.000001) so budgets never touch
// floating point.
//
// State lives only in this process's memory: it does not survive a
// restart and is not shared across multiple Governor instances. Use
// RedisStore instead if either of those matters to you.
type MemoryStore struct {
	mu    sync.Mutex
	spent map[string]int64
}

// NewMemoryStore returns a ready-to-use in-memory Store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{spent: make(map[string]int64)}
}

var _ Store = (*MemoryStore)(nil)

// Reserve locks, checks, and commits in one critical section — the same
// atomicity RedisStore gets from a Lua script, just without the network
// hop, since there's only one process to serialize against.
func (m *MemoryStore) Reserve(_ context.Context, key string, amountMicros, capMicros int64) (Result, error) {
	if amountMicros < 0 {
		return Result{}, errors.New("budget: amountMicros must be >= 0")
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	current := m.spent[key]
	if current+amountMicros > capMicros {
		return Result{Allowed: false, SpentMicros: current}, nil
	}
	current += amountMicros
	m.spent[key] = current
	return Result{Allowed: true, SpentMicros: current}, nil
}

// Refund reduces key's running total by deltaMicros.
func (m *MemoryStore) Refund(_ context.Context, key string, deltaMicros int64) error {
	if deltaMicros < 0 {
		return errors.New("budget: deltaMicros must be >= 0")
	}
	if deltaMicros == 0 {
		return nil
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	m.spent[key] -= deltaMicros
	return nil
}
