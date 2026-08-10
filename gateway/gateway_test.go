package gateway_test

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hadi-moustafa/governor/gateway"
	"github.com/hadi-moustafa/governor/internal/mockprovider"
)

const reqJSON = `{"model":"fake","messages":[{"role":"user","content":"hi"}]}`

func TestNew_ZeroConfigDefaultsToMockProvider(t *testing.T) {
	mp := mockprovider.NewServer(mockprovider.Config{Chunks: 3, TokensPerChunk: 5, ChunkDelay: time.Millisecond})
	upstream := httptest.NewServer(mp)
	defer upstream.Close()

	gw, err := gateway.New(gateway.Config{ProviderURL: upstream.URL})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	rec := httptest.NewRecorder()
	gw.ServeHTTP(rec, httptest.NewRequest("POST", "/", strings.NewReader(reqJSON)))

	gotChunks := strings.Count(rec.Body.String(), `"finish_reason":""`)
	if gotChunks != 3 {
		t.Fatalf("client received %d chunks, want 3", gotChunks)
	}
	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

func TestNew_DebugMetricsEndpointReflectsTraffic(t *testing.T) {
	mp := mockprovider.NewServer(mockprovider.Config{Chunks: 2, TokensPerChunk: 5, ChunkDelay: time.Millisecond})
	upstream := httptest.NewServer(mp)
	defer upstream.Close()

	gw, err := gateway.New(gateway.Config{ProviderURL: upstream.URL, CapDollars: 0.0001}) // tiny cap, forces a preflight denial
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	gw.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("POST", "/", strings.NewReader(reqJSON)))

	rec := httptest.NewRecorder()
	gw.ServeHTTP(rec, httptest.NewRequest("GET", "/debug/metrics", nil))

	var snap struct {
		PreflightDenials int64 `json:"preflight_denials"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &snap); err != nil {
		t.Fatalf("decoding /debug/metrics response: %v", err)
	}
	if snap.PreflightDenials != 1 {
		t.Fatalf("preflight_denials = %d, want 1", snap.PreflightDenials)
	}

	// Also reachable directly, without an HTTP round trip.
	if got := gw.Metrics.Snapshot().PreflightDenials; got != 1 {
		t.Fatalf("gw.Metrics.Snapshot().PreflightDenials = %d, want 1", got)
	}
}

func TestNew_UnknownProviderReturnsError(t *testing.T) {
	_, err := gateway.New(gateway.Config{Provider: "not-a-real-provider"})
	if err == nil {
		t.Fatal("expected an error for an unknown provider")
	}
}

func TestNew_OpenAIWithoutAPIKeyReturnsError(t *testing.T) {
	_, err := gateway.New(gateway.Config{Provider: "openai"})
	if err == nil {
		t.Fatal("expected an error when Provider=openai has no OpenAIAPIKey")
	}
}

func TestNew_AnthropicWithoutAPIKeyReturnsError(t *testing.T) {
	_, err := gateway.New(gateway.Config{Provider: "anthropic"})
	if err == nil {
		t.Fatal("expected an error when Provider=anthropic has no AnthropicAPIKey")
	}
}
