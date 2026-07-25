package mockprovider_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hadi-moustafa/governor/internal/mockprovider"
)

func TestServer_CompletesWhenNotCanceled(t *testing.T) {
	const chunks = 5
	srv := mockprovider.NewServer(mockprovider.Config{
		Chunks:         chunks,
		TokensPerChunk: 8,
		ChunkDelay:     5 * time.Millisecond,
	})
	ts := httptest.NewServer(srv)
	defer ts.Close()

	req, err := http.NewRequest(http.MethodPost, ts.URL, nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	// Drain fully so the handler runs to completion before we Await.
	if _, err := readAll(resp); err != nil {
		t.Fatalf("reading response: %v", err)
	}

	stats, err := srv.Await(2 * time.Second)
	if err != nil {
		t.Fatalf("Await: %v", err)
	}
	if !stats.Completed {
		t.Fatal("expected Completed=true when the client reads the full stream")
	}
	if stats.CanceledByClient {
		t.Fatal("expected CanceledByClient=false when the client reads the full stream")
	}
	if stats.ChunksWritten != chunks {
		t.Fatalf("ChunksWritten = %d, want %d", stats.ChunksWritten, chunks)
	}
}

func TestServer_ObservesClientCancellation(t *testing.T) {
	const chunks = 30
	srv := mockprovider.NewServer(mockprovider.Config{
		Chunks:         chunks,
		TokensPerChunk: 8,
		ChunkDelay:     15 * time.Millisecond,
	})
	ts := httptest.NewServer(srv)
	defer ts.Close()

	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ts.URL, nil)
	if err != nil {
		t.Fatalf("NewRequestWithContext: %v", err)
	}
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}

	// Let a few chunks land, then really tear down the connection.
	buf := make([]byte, 512)
	if _, err := resp.Body.Read(buf); err != nil {
		t.Fatalf("initial read: %v", err)
	}
	cancel()
	resp.Body.Close()

	stats, err := srv.Await(2 * time.Second)
	if err != nil {
		t.Fatalf("Await: %v", err)
	}
	if stats.Completed {
		t.Fatal("expected Completed=false after canceling the client request")
	}
	if !stats.CanceledByClient {
		t.Fatal("expected CanceledByClient=true after canceling the client request")
	}
	if stats.ChunksWritten >= chunks {
		t.Fatalf("ChunksWritten = %d, want fewer than the full %d", stats.ChunksWritten, chunks)
	}
	maxDuration := time.Duration(chunks) * 15 * time.Millisecond
	if stats.HandlerDuration >= maxDuration {
		t.Fatalf("HandlerDuration = %v, want well under the full-completion time %v", stats.HandlerDuration, maxDuration)
	}
}

func TestServer_SummaryOnlyReportsUsageOnLastChunkOnly(t *testing.T) {
	const chunks = 4
	const tokensPerChunk = 7
	srv := mockprovider.NewServer(mockprovider.Config{
		Chunks:         chunks,
		TokensPerChunk: tokensPerChunk,
		ChunkDelay:     time.Millisecond,
		SummaryOnly:    true,
	})
	ts := httptest.NewServer(srv)
	defer ts.Close()

	req, err := http.NewRequest(http.MethodPost, ts.URL, nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading response: %v", err)
	}

	if got := strings.Count(string(body), `"tokens":0`); got != chunks-1 {
		t.Fatalf("chunks reporting tokens:0 = %d, want %d (all but the last)", got, chunks-1)
	}
	want := fmt.Sprintf(`"tokens":%d`, tokensPerChunk*chunks)
	if !strings.Contains(string(body), want) {
		t.Fatalf("response body does not contain the cumulative total %q:\n%s", want, body)
	}
}

func TestServer_FailAfterChunkEmitsErrorEvent(t *testing.T) {
	srv := mockprovider.NewServer(mockprovider.Config{
		Chunks:         10,
		TokensPerChunk: 5,
		ChunkDelay:     time.Millisecond,
		FailAfterChunk: 2,
	})
	ts := httptest.NewServer(srv)
	defer ts.Close()

	req, err := http.NewRequest(http.MethodPost, ts.URL, nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading response: %v", err)
	}

	if !strings.Contains(string(body), `"error"`) {
		t.Fatalf("expected an error event in the response body, got:\n%s", body)
	}
	if strings.Contains(string(body), "[DONE]") {
		t.Fatal("did not expect a [DONE] terminator after a simulated failure")
	}

	stats, err := srv.Await(2 * time.Second)
	if err != nil {
		t.Fatalf("Await: %v", err)
	}
	if stats.ChunksWritten != 2 {
		t.Fatalf("ChunksWritten = %d, want 2 (stopped before the failing chunk)", stats.ChunksWritten)
	}
	if stats.Completed {
		t.Fatal("expected Completed=false after a simulated upstream failure")
	}
}

func readAll(resp *http.Response) (int, error) {
	buf := make([]byte, 4096)
	total := 0
	for {
		n, err := resp.Body.Read(buf)
		total += n
		if err != nil {
			if errors.Is(err, io.EOF) {
				return total, nil
			}
			return total, err
		}
	}
}
