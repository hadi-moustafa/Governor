package proxy_test

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hadi-moustafa/governor/internal/budget"
	"github.com/hadi-moustafa/governor/internal/ledger"
	"github.com/hadi-moustafa/governor/internal/metrics"
	"github.com/hadi-moustafa/governor/internal/mockprovider"
	"github.com/hadi-moustafa/governor/internal/pricing"
	"github.com/hadi-moustafa/governor/internal/provider/mock"
	"github.com/hadi-moustafa/governor/internal/provider/mockfinal"
	"github.com/hadi-moustafa/governor/internal/proxy"
)

const reqJSON = `{"model":"fake","messages":[{"role":"user","content":"hi"}]}`

func TestServeHTTP_FullPassthroughWhenUnderCap(t *testing.T) {
	const chunks = 5
	mp := mockprovider.NewServer(mockprovider.Config{
		Chunks:         chunks,
		TokensPerChunk: 10,
		ChunkDelay:     5 * time.Millisecond,
	})
	upstream := httptest.NewServer(mp)
	defer upstream.Close()

	h := &proxy.Handler{
		Provider:        mock.New(upstream.URL),
		Store:           budget.NewMemoryStore(),
		Pricing:         pricing.Model{MicrosPerToken: 1000},
		CapMicros:       1_000_000, // generous — the whole stream costs 5*10*1000=50,000 micros
		PreflightTokens: 1,
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/", strings.NewReader(reqJSON)))

	body := rec.Body.String()
	gotChunks := strings.Count(body, "\"finish_reason\":\"\"")
	if gotChunks != chunks {
		t.Fatalf("client received %d chunks, want all %d", gotChunks, chunks)
	}
	if strings.Contains(body, "budget_exceeded") {
		t.Fatal("did not expect a budget_exceeded event under a generous cap")
	}

	stats, err := mp.Await(2 * time.Second)
	if err != nil {
		t.Fatalf("mock provider never finished handling the request: %v", err)
	}
	if !stats.Completed {
		t.Fatal("expected the mock provider to run to completion under a generous cap")
	}
	if stats.CanceledByClient {
		t.Fatal("did not expect the mock provider to observe cancellation under a generous cap")
	}
}

// TestServeHTTP_CutoffCancelsUpstream is the crux of this milestone: a
// budget denial mid-stream must cancel the upstream provider request, not
// just stop forwarding to the client. It's asserted from *inside* the fake
// provider — stats it records only because it independently observed its
// own request context being canceled, which net/http only does when the
// underlying TCP connection is actually torn down.
func TestServeHTTP_CutoffCancelsUpstream(t *testing.T) {
	const totalChunks = 30
	mp := mockprovider.NewServer(mockprovider.Config{
		Chunks:         totalChunks,
		TokensPerChunk: 10,
		ChunkDelay:     15 * time.Millisecond,
	})
	upstream := httptest.NewServer(mp)
	defer upstream.Close()

	h := &proxy.Handler{
		Provider: mock.New(upstream.URL),
		Store:    budget.NewMemoryStore(),
		Pricing:  pricing.Model{MicrosPerToken: 1000},
		// preflight (1 tok = 1,000 micros) + 5 full chunks (10 tok =
		// 10,000 micros each) = 51,000; the 6th chunk (would bring the
		// total to 61,000) is denied.
		CapMicros:       51_000,
		PreflightTokens: 1,
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/", strings.NewReader(reqJSON)))

	// Client-facing assertions: the stream was actually truncated.
	body := rec.Body.String()
	gotChunks := strings.Count(body, "\"finish_reason\":\"\"")
	if gotChunks >= totalChunks {
		t.Fatalf("client received the full stream (%d chunks) — cap had no client-visible effect", gotChunks)
	}
	if !strings.Contains(body, "budget_exceeded") {
		t.Fatal("client stream did not contain a budget_exceeded cutoff event")
	}

	// THE assertion: what did the upstream mock provider itself observe?
	stats, err := mp.Await(2 * time.Second)
	if err != nil {
		t.Fatalf("mock provider never finished handling the request: %v", err)
	}
	if stats.Completed {
		t.Fatal("mock provider ran to completion — the upstream request was never canceled, so Governor kept paying for tokens after the client-facing cutoff")
	}
	if !stats.CanceledByClient {
		t.Fatal("mock provider did not observe the client disconnecting — the upstream connection was never actually torn down")
	}
	if stats.ChunksWritten >= totalChunks/2 {
		t.Fatalf("mock provider wrote %d/%d chunks before stopping — cutoff was too late to matter", stats.ChunksWritten, totalChunks)
	}
	maxDuration := time.Duration(totalChunks) * 15 * time.Millisecond
	if stats.HandlerDuration >= maxDuration {
		t.Fatalf("mock provider took %v to notice cancellation, want well under the full-completion time %v", stats.HandlerDuration, maxDuration)
	}
}

// TestServeHTTP_KeyFromHeader proves distinct X-Governor-Key values are
// tracked as independent budgets, and requests without the header still
// fall back to the default key.
func TestServeHTTP_KeyFromHeader(t *testing.T) {
	mp := mockprovider.NewServer(mockprovider.Config{
		Chunks:         2,
		TokensPerChunk: 10,
		ChunkDelay:     5 * time.Millisecond,
	})
	upstream := httptest.NewServer(mp)
	defer upstream.Close()

	store := budget.NewMemoryStore()
	h := &proxy.Handler{
		Provider: mock.New(upstream.URL),
		Store:    store,
		Pricing:  pricing.Model{MicrosPerToken: 1000},
		// preflight (1,000) + 2 chunks (10,000 each) = 21,000 exactly —
		// enough for one full stream per key, not two under the same key.
		CapMicros:       21_000,
		PreflightTokens: 1,
	}

	reqA := httptest.NewRequest("POST", "/", strings.NewReader(reqJSON))
	reqA.Header.Set("X-Governor-Key", "tenant-a")
	recA := httptest.NewRecorder()
	h.ServeHTTP(recA, reqA)
	if strings.Contains(recA.Body.String(), "budget_exceeded") {
		t.Fatal("tenant-a's first request should not hit its own cap")
	}

	reqB := httptest.NewRequest("POST", "/", strings.NewReader(reqJSON))
	reqB.Header.Set("X-Governor-Key", "tenant-b")
	recB := httptest.NewRecorder()
	h.ServeHTTP(recB, reqB)
	if strings.Contains(recB.Body.String(), "budget_exceeded") {
		t.Fatal("tenant-b should have its own independent budget, not share tenant-a's")
	}

	// No header at all — falls back to the default key, which is now
	// exhausted by neither tenant-a nor tenant-b (they're separate keys),
	// so this should also succeed.
	reqDefault := httptest.NewRequest("POST", "/", strings.NewReader(reqJSON))
	recDefault := httptest.NewRecorder()
	h.ServeHTTP(recDefault, reqDefault)
	if strings.Contains(recDefault.Body.String(), "budget_exceeded") {
		t.Fatal("request with no key header should succeed under the fresh default key")
	}

	// A second request under the default key, with nothing left in its cap,
	// should now be denied — proving the default key persisted across the
	// no-header request above rather than being reset.
	reqDefault2 := httptest.NewRequest("POST", "/", strings.NewReader(reqJSON))
	recDefault2 := httptest.NewRecorder()
	h.ServeHTTP(recDefault2, reqDefault2)
	if recDefault2.Code != 429 {
		t.Fatalf("status = %d, want 429 (default key's budget should already be exhausted)", recDefault2.Code)
	}
}

// TestServeHTTP_SummaryOnlyProviderCannotBeCutMidStream documents a real
// limitation, not a bug in the test: internal/proxy/stream.go reserves
// budget per chunk, using each chunk's own Tokens value. A provider that
// only reports usage on its final chunk (SummaryOnly) reserves $0 for every
// intermediate chunk, so a cap sized to trip mid-stream against a normal
// per-chunk-reporting provider can only possibly deny the *last* chunk here
// — by which point the entire body has already been forwarded to the
// client. Live mid-stream cutoff is structurally impossible against a
// provider shaped like this; at best the denial arrives too late to matter.
// This is the finding the mockfinal provider exists to surface ahead of
// Phase 2's real provider adapters (this is exactly OpenAI's streaming
// usage-reporting shape).
func TestServeHTTP_SummaryOnlyProviderCannotBeCutMidStream(t *testing.T) {
	const totalChunks = 30
	mp := mockprovider.NewServer(mockprovider.Config{
		Chunks:         totalChunks,
		TokensPerChunk: 10,
		ChunkDelay:     2 * time.Millisecond,
		SummaryOnly:    true,
	})
	upstream := httptest.NewServer(mp)
	defer upstream.Close()

	h := &proxy.Handler{
		Provider: mockfinal.New(upstream.URL),
		Store:    budget.NewMemoryStore(),
		Pricing:  pricing.Model{MicrosPerToken: 1000},
		// Same cap that trips TestServeHTTP_CutoffCancelsUpstream well
		// before the stream ends against a per-chunk-reporting provider.
		CapMicros:       51_000,
		PreflightTokens: 1,
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/", strings.NewReader(reqJSON)))

	// Every intermediate chunk carries Tokens:0, so all totalChunks-1 of
	// them are reserved for free and reach the client no matter how low
	// the cap is. Only the final, cumulative-total chunk has a real cost
	// (30*10*1000 = 300,000 micros) — well over the 51,000 cap — so it's
	// the one and only chunk that can be denied, and only after
	// everything ahead of it already shipped.
	body := rec.Body.String()
	gotChunks := strings.Count(body, "\"finish_reason\":\"\"")
	if gotChunks != totalChunks-1 {
		t.Fatalf("client received %d/%d chunks, want exactly %d (every chunk but the cumulative-total last one, which is the only one with a nonzero, cap-tripping cost)", gotChunks, totalChunks, totalChunks-1)
	}
	if !strings.Contains(body, "budget_exceeded") {
		t.Fatal("expected the final chunk to be denied — it's the only chunk whose reserved cost isn't 0")
	}

	if _, err := mp.Await(2 * time.Second); err != nil {
		t.Fatalf("mock provider never finished handling the request: %v", err)
	}
}

func TestServeHTTP_PreflightDenialNeverCallsProvider(t *testing.T) {
	mp := mockprovider.NewServer(mockprovider.Config{
		Chunks:         5,
		TokensPerChunk: 10,
		ChunkDelay:     5 * time.Millisecond,
	})
	upstream := httptest.NewServer(mp)
	defer upstream.Close()

	h := &proxy.Handler{
		Provider:        mock.New(upstream.URL),
		Store:           budget.NewMemoryStore(),
		Pricing:         pricing.Model{MicrosPerToken: 1000},
		CapMicros:       500, // less than the preflight reservation itself
		PreflightTokens: 1,
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/", strings.NewReader(reqJSON)))

	if rec.Code != 429 {
		t.Fatalf("status = %d, want 429", rec.Code)
	}

	// The provider must never have been reached at all — give it a moment
	// then confirm Await times out (no request ever arrived to complete).
	if _, err := mp.Await(100 * time.Millisecond); err == nil {
		t.Fatal("expected mock provider to never receive a request when preflight is denied")
	}
}

// TestServeHTTP_ReconciliationNoDriftOnNormalCompletion proves the common
// case: when a stream runs to completion normally, everything reserved was
// for content actually delivered, so the ledger shows zero drift and
// nothing gets refunded.
func TestServeHTTP_ReconciliationNoDriftOnNormalCompletion(t *testing.T) {
	mp := mockprovider.NewServer(mockprovider.Config{
		Chunks:         5,
		TokensPerChunk: 10,
		ChunkDelay:     time.Millisecond,
	})
	upstream := httptest.NewServer(mp)
	defer upstream.Close()

	store := budget.NewMemoryStore()
	led := ledger.NewMemoryStore()
	h := &proxy.Handler{
		Provider:        mock.New(upstream.URL),
		Store:           store,
		Ledger:          led,
		Pricing:         pricing.Model{MicrosPerToken: 1000},
		CapMicros:       1_000_000,
		PreflightTokens: 1,
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/", strings.NewReader(reqJSON)))

	records := led.Records()
	if len(records) != 1 {
		t.Fatalf("got %d ledger records, want 1", len(records))
	}
	r := records[0]
	if r.FinishReason != "stop" {
		t.Fatalf("FinishReason = %q, want %q", r.FinishReason, "stop")
	}
	if r.EstimatedMicros != r.ActualMicros {
		t.Fatalf("EstimatedMicros=%d != ActualMicros=%d — expected zero drift when every chunk's real cost was known as it arrived", r.EstimatedMicros, r.ActualMicros)
	}
	want := int64(1000 + 5*10*1000) // preflight + 5 chunks * 10 tokens * 1000 micros/token
	if r.EstimatedMicros != want {
		t.Fatalf("EstimatedMicros = %d, want %d", r.EstimatedMicros, want)
	}

	// No refund should have happened: the key's running total should
	// still equal the full estimated cost.
	res, err := store.Reserve(context.Background(), "default", 0, 1_000_000)
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	if res.SpentMicros != want {
		t.Fatalf("SpentMicros = %d, want %d (unchanged — nothing should have been refunded)", res.SpentMicros, want)
	}
}

// TestServeHTTP_ReconciliationRefundsWastedPreflightOnImmediateFailure
// proves the case Store.Refund exists for: if the upstream provider fails
// before ever streaming a single chunk, the preflight reservation bought
// zero real content, so reconciliation must refund it in full.
func TestServeHTTP_ReconciliationRefundsWastedPreflightOnImmediateFailure(t *testing.T) {
	mp := mockprovider.NewServer(mockprovider.Config{
		Chunks:          10,
		TokensPerChunk:  10,
		ChunkDelay:      time.Millisecond,
		FailImmediately: true, // zero content ever streamed
	})
	upstream := httptest.NewServer(mp)
	defer upstream.Close()

	store := budget.NewMemoryStore()
	led := ledger.NewMemoryStore()
	const preflightMicros = 1000
	h := &proxy.Handler{
		Provider:        mockfinal.New(upstream.URL),
		Store:           store,
		Ledger:          led,
		Pricing:         pricing.Model{MicrosPerToken: 1000},
		CapMicros:       1_000_000,
		PreflightTokens: 1, // preflightMicros = 1 * 1000
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/", strings.NewReader(reqJSON)))

	records := led.Records()
	if len(records) != 1 {
		t.Fatalf("got %d ledger records, want 1", len(records))
	}
	r := records[0]
	if r.FinishReason != "provider_error" {
		t.Fatalf("FinishReason = %q, want %q", r.FinishReason, "provider_error")
	}
	if r.EstimatedMicros != preflightMicros {
		t.Fatalf("EstimatedMicros = %d, want %d (the wasted preflight reservation)", r.EstimatedMicros, preflightMicros)
	}
	if r.ActualMicros != 0 {
		t.Fatalf("ActualMicros = %d, want 0 (no content was ever delivered)", r.ActualMicros)
	}

	// The full preflight reservation should have been refunded: a
	// follow-up reservation for exactly the cap should now be allowed.
	res, err := store.Reserve(context.Background(), "default", 1_000_000, 1_000_000)
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	if !res.Allowed {
		t.Fatalf("expected the full cap to be reservable after the preflight was refunded, got SpentMicros=%d", res.SpentMicros)
	}
}

// TestServeHTTP_MetricsCountPreflightDenialAndCutoff proves the metrics
// counters that matter most for a spend-control gateway actually
// increment at the two decision points they exist to observe: a request
// denied before ever reaching a provider, and a stream cut off mid-flight.
func TestServeHTTP_MetricsCountPreflightDenialAndCutoff(t *testing.T) {
	m := &metrics.Counters{}

	t.Run("preflight denial", func(t *testing.T) {
		mp := mockprovider.NewServer(mockprovider.Config{Chunks: 5, TokensPerChunk: 10, ChunkDelay: time.Millisecond})
		upstream := httptest.NewServer(mp)
		defer upstream.Close()

		h := &proxy.Handler{
			Provider:        mock.New(upstream.URL),
			Store:           budget.NewMemoryStore(),
			Pricing:         pricing.Model{MicrosPerToken: 1000},
			Metrics:         m,
			CapMicros:       500, // less than the preflight reservation itself
			PreflightTokens: 1,
		}
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("POST", "/", strings.NewReader(reqJSON)))

		if got := m.Snapshot().PreflightDenials; got != 1 {
			t.Fatalf("PreflightDenials = %d, want 1", got)
		}
	})

	t.Run("mid-stream cutoff", func(t *testing.T) {
		mp := mockprovider.NewServer(mockprovider.Config{Chunks: 30, TokensPerChunk: 10, ChunkDelay: 15 * time.Millisecond})
		upstream := httptest.NewServer(mp)
		defer upstream.Close()

		h := &proxy.Handler{
			Provider:        mock.New(upstream.URL),
			Store:           budget.NewMemoryStore(),
			Pricing:         pricing.Model{MicrosPerToken: 1000},
			Metrics:         m,
			CapMicros:       51_000,
			PreflightTokens: 1,
		}
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("POST", "/", strings.NewReader(reqJSON)))

		if got := m.Snapshot().MidStreamCutoffs; got != 1 {
			t.Fatalf("MidStreamCutoffs = %d, want 1", got)
		}
	})
}

// TestServeHTTP_MetricsCountReconciliationRefund proves DriftMicrosTotal
// and RefundsIssued increment exactly when reconcile actually refunds
// something — the immediate-failure case, same scenario as
// TestServeHTTP_ReconciliationRefundsWastedPreflightOnImmediateFailure.
func TestServeHTTP_MetricsCountReconciliationRefund(t *testing.T) {
	mp := mockprovider.NewServer(mockprovider.Config{
		Chunks:          10,
		TokensPerChunk:  10,
		ChunkDelay:      time.Millisecond,
		FailImmediately: true,
	})
	upstream := httptest.NewServer(mp)
	defer upstream.Close()

	m := &metrics.Counters{}
	h := &proxy.Handler{
		Provider:        mockfinal.New(upstream.URL),
		Store:           budget.NewMemoryStore(),
		Ledger:          ledger.NewMemoryStore(),
		Metrics:         m,
		Pricing:         pricing.Model{MicrosPerToken: 1000},
		CapMicros:       1_000_000,
		PreflightTokens: 1,
	}
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("POST", "/", strings.NewReader(reqJSON)))

	snap := m.Snapshot()
	if snap.Reconciliations != 1 {
		t.Fatalf("Reconciliations = %d, want 1", snap.Reconciliations)
	}
	if snap.RefundsIssued != 1 {
		t.Fatalf("RefundsIssued = %d, want 1", snap.RefundsIssued)
	}
	if snap.DriftMicrosTotal != 1000 {
		t.Fatalf("DriftMicrosTotal = %d, want 1000 (the wasted preflight reservation)", snap.DriftMicrosTotal)
	}
}
