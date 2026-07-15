package proxy_test

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hadi-moustafa/governor/internal/budget"
	"github.com/hadi-moustafa/governor/internal/mockprovider"
	"github.com/hadi-moustafa/governor/internal/pricing"
	"github.com/hadi-moustafa/governor/internal/provider/mock"
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
