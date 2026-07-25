package ledger

import (
	"context"
	"sync"
)

// MemoryStore is an in-process Store: a mutex-guarded slice of Records.
// This is the default backend — it's enough to exercise and observe
// reconciliation without any external service, at the cost of not
// surviving a restart. Use PostgresStore instead if durability matters.
type MemoryStore struct {
	mu      sync.Mutex
	records []Record
}

// NewMemoryStore returns a ready-to-use in-memory Store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{}
}

var _ Store = (*MemoryStore)(nil)

// Record implements Store.
func (m *MemoryStore) Record(_ context.Context, r Record) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.records = append(m.records, r)
	return nil
}

// Records returns a copy of every Record stored so far, oldest first.
func (m *MemoryStore) Records() []Record {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Record, len(m.records))
	copy(out, m.records)
	return out
}
