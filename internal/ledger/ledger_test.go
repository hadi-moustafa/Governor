package ledger_test

import (
	"context"
	"testing"
	"time"

	"github.com/hadi-moustafa/governor/internal/ledger"
)

func TestMemoryStore_RecordAppendsInOrder(t *testing.T) {
	store := ledger.NewMemoryStore()
	ctx := context.Background()

	want := []ledger.Record{
		{Key: "a", EstimatedMicros: 1000, ActualMicros: 1000, FinishReason: "stop", RecordedAt: time.Now()},
		{Key: "b", EstimatedMicros: 5000, ActualMicros: 3000, FinishReason: "budget_exceeded", RecordedAt: time.Now()},
	}
	for _, r := range want {
		if err := store.Record(ctx, r); err != nil {
			t.Fatalf("Record: %v", err)
		}
	}

	got := store.Records()
	if len(got) != len(want) {
		t.Fatalf("Records() returned %d records, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].Key != want[i].Key || got[i].EstimatedMicros != want[i].EstimatedMicros || got[i].ActualMicros != want[i].ActualMicros || got[i].FinishReason != want[i].FinishReason {
			t.Fatalf("Records()[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}
