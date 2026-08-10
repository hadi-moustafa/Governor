package metrics_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hadi-moustafa/governor/internal/metrics"
)

func TestCounters_SnapshotReflectsAdds(t *testing.T) {
	c := &metrics.Counters{}
	c.PreflightDenials.Add(3)
	c.MidStreamCutoffs.Add(1)
	c.StreamsCompleted.Add(5)
	c.Reconciliations.Add(6)
	c.RefundsIssued.Add(2)
	c.DriftMicrosTotal.Add(1500)

	snap := c.Snapshot()
	if snap.PreflightDenials != 3 {
		t.Errorf("PreflightDenials = %d, want 3", snap.PreflightDenials)
	}
	if snap.MidStreamCutoffs != 1 {
		t.Errorf("MidStreamCutoffs = %d, want 1", snap.MidStreamCutoffs)
	}
	if snap.StreamsCompleted != 5 {
		t.Errorf("StreamsCompleted = %d, want 5", snap.StreamsCompleted)
	}
	if snap.Reconciliations != 6 {
		t.Errorf("Reconciliations = %d, want 6", snap.Reconciliations)
	}
	if snap.RefundsIssued != 2 {
		t.Errorf("RefundsIssued = %d, want 2", snap.RefundsIssued)
	}
	if snap.DriftMicrosTotal != 1500 {
		t.Errorf("DriftMicrosTotal = %d, want 1500", snap.DriftMicrosTotal)
	}
}

func TestHandler_ServesSnapshotAsJSON(t *testing.T) {
	c := &metrics.Counters{}
	c.PreflightDenials.Add(7)

	rec := httptest.NewRecorder()
	metrics.Handler(c).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	var snap metrics.Snapshot
	if err := json.Unmarshal(rec.Body.Bytes(), &snap); err != nil {
		t.Fatalf("decoding response body: %v", err)
	}
	if snap.PreflightDenials != 7 {
		t.Errorf("PreflightDenials = %d, want 7", snap.PreflightDenials)
	}
}
