package ledger_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/hadi-moustafa/governor/internal/ledger"
)

// TestPostgresStore_AgainstRealPostgres proves PostgresStore actually
// works against real Postgres, not just that it compiles. Skipped unless
// GOVERNOR_TEST_POSTGRES_DSN is set, so `go test ./...` never requires
// Docker/Postgres to be running. Point it at the postgres service in the
// repo's docker-compose.yml, e.g.:
//
//	GOVERNOR_TEST_POSTGRES_DSN="postgres://governor:governor@localhost:5432/governor" \
//	  go test ./internal/ledger/...
func TestPostgresStore_AgainstRealPostgres(t *testing.T) {
	dsn := os.Getenv("GOVERNOR_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("GOVERNOR_TEST_POSTGRES_DSN not set — skipping real-Postgres integration test (see docker-compose.yml)")
	}

	ctx := context.Background()
	store, err := ledger.NewPostgresStore(ctx, dsn)
	if err != nil {
		t.Fatalf("NewPostgresStore: %v", err)
	}
	defer store.Close()

	r := ledger.Record{
		Key:             "governor-test:" + t.Name(),
		EstimatedMicros: 51_000,
		ActualMicros:    41_000,
		FinishReason:    "budget_exceeded",
		RecordedAt:      time.Now().UTC(),
	}
	if err := store.Record(ctx, r); err != nil {
		t.Fatalf("Record: %v", err)
	}
}
