package ledger

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// createTableSQL is run once by NewPostgresStore so a fresh database is
// ready to use with no separate migration step — consistent with the rest
// of this project's "just enough" config story (no migration framework,
// no ORM).
const createTableSQL = `
CREATE TABLE IF NOT EXISTS ledger_records (
	id                BIGSERIAL PRIMARY KEY,
	key               TEXT NOT NULL,
	estimated_micros  BIGINT NOT NULL,
	actual_micros     BIGINT NOT NULL,
	finish_reason     TEXT NOT NULL,
	recorded_at       TIMESTAMPTZ NOT NULL
)`

const insertSQL = `
INSERT INTO ledger_records (key, estimated_micros, actual_micros, finish_reason, recorded_at)
VALUES ($1, $2, $3, $4, $5)`

// PostgresStore is the durable Store: use it when reconciled cost records
// need to survive a restart or be visible outside the Governor process.
// A single Governor process doesn't need this to exercise or observe
// reconciliation — see MemoryStore, the default.
type PostgresStore struct {
	pool *pgxpool.Pool
}

// NewPostgresStore connects to Postgres using dsn (e.g.
// "postgres://governor:governor@localhost:5432/governor") and ensures the
// ledger_records table exists. The caller owns the returned pool's
// lifecycle (including Close).
func NewPostgresStore(ctx context.Context, dsn string) (*PostgresStore, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("ledger: connect: %w", err)
	}
	if _, err := pool.Exec(ctx, createTableSQL); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ledger: create table: %w", err)
	}
	return &PostgresStore{pool: pool}, nil
}

var _ Store = (*PostgresStore)(nil)

// Record implements Store.
func (s *PostgresStore) Record(ctx context.Context, r Record) error {
	_, err := s.pool.Exec(ctx, insertSQL, r.Key, r.EstimatedMicros, r.ActualMicros, r.FinishReason, r.RecordedAt)
	if err != nil {
		return fmt.Errorf("ledger: record: %w", err)
	}
	return nil
}

// Close releases the underlying connection pool.
func (s *PostgresStore) Close() {
	s.pool.Close()
}
