package anthropic_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hadi-moustafa/governor/internal/provider"
	"github.com/hadi-moustafa/governor/internal/provider/anthropic"
)

// TestSend_ParsesRealEventSequence emulates the actual sequence of named
// SSE events the Messages API sends: message_start, content_block_start,
// several content_block_delta text chunks, content_block_stop,
// message_delta (carrying stop_reason and the cumulative output token
// count), then message_stop with the connection simply closing — no
// literal terminal sentinel the way OpenAI/the mock provider use [DONE].
func TestSend_ParsesRealEventSequence(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("x-api-key"); got != "ak-test" {
			t.Errorf("x-api-key header = %q, want ak-test", got)
		}
		if got := r.Header.Get("anthropic-version"); got == "" {
			t.Error("anthropic-version header missing")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `event: message_start`+"\n"+`data: {"type":"message_start","message":{"id":"msg_1","model":"claude-test","usage":{"input_tokens":10,"output_tokens":1}}}`+"\n\n")
		fmt.Fprint(w, `event: content_block_start`+"\n"+`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`+"\n\n")
		fmt.Fprint(w, `event: ping`+"\n"+`data: {"type":"ping"}`+"\n\n")
		fmt.Fprint(w, `event: content_block_delta`+"\n"+`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hel"}}`+"\n\n")
		fmt.Fprint(w, `event: content_block_delta`+"\n"+`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"lo"}}`+"\n\n")
		fmt.Fprint(w, `event: content_block_stop`+"\n"+`data: {"type":"content_block_stop","index":0}`+"\n\n")
		fmt.Fprint(w, `event: message_delta`+"\n"+`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}`+"\n\n")
		fmt.Fprint(w, `event: message_stop`+"\n"+`data: {"type":"message_stop"}`+"\n\n")
	}))
	defer ts.Close()

	c := &anthropic.Client{APIKey: "ak-test", BaseURL: ts.URL}
	s, err := c.Send(context.Background(), provider.Request{
		Model: "claude-test",
		Messages: []provider.Message{
			{Role: "system", Content: "be terse"},
			{Role: "user", Content: "hi"},
		},
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	defer s.Close()

	// message_start, content_block_start, and ping are all skipped —
	// the first chunk Next returns should be the first text delta.
	chunk1, err := s.Next()
	if err != nil {
		t.Fatalf("Next (1): %v", err)
	}
	if chunk1.Delta != "Hel" || chunk1.Tokens != 0 {
		t.Fatalf("chunk1 = %+v, want Delta=Hel Tokens=0", chunk1)
	}

	chunk2, err := s.Next()
	if err != nil {
		t.Fatalf("Next (2): %v", err)
	}
	if chunk2.Delta != "lo" || chunk2.Tokens != 0 {
		t.Fatalf("chunk2 = %+v, want Delta=lo Tokens=0", chunk2)
	}

	// content_block_stop is skipped; message_delta is next and carries
	// both the finish reason and the real (incremental, not
	// end-of-stream-only) token count.
	chunk3, err := s.Next()
	if err != nil {
		t.Fatalf("Next (message_delta): %v", err)
	}
	if chunk3.FinishReason != "end_turn" || chunk3.Tokens != 2 {
		t.Fatalf("message_delta chunk = %+v, want FinishReason=end_turn Tokens=2", chunk3)
	}

	// message_stop carries nothing, and the server then closes the
	// connection — the next read should surface a real EOF, not a
	// literal sentinel value.
	if _, err := s.Next(); err != io.EOF {
		t.Fatalf("Next (after message_stop) err = %v, want io.EOF", err)
	}
}

func TestSend_ErrorStatusReturnsTypedAPIError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		fmt.Fprint(w, `{"error":{"type":"rate_limit_error","message":"Number of request tokens has exceeded your per-minute rate limit"}}`)
	}))
	defer ts.Close()

	c := &anthropic.Client{APIKey: "ak-test", BaseURL: ts.URL}
	_, err := c.Send(context.Background(), provider.Request{Model: "claude-test"})
	if err == nil {
		t.Fatal("expected an error for a 429 response")
	}
	var apiErr *anthropic.APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("err = %v (%T), want *anthropic.APIError", err, err)
	}
	if apiErr.Type != "rate_limit_error" {
		t.Errorf("Type = %q, want rate_limit_error", apiErr.Type)
	}
}

func TestSend_MidStreamErrorEventReturnsTypedAPIError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `event: error`+"\n"+`data: {"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`+"\n\n")
	}))
	defer ts.Close()

	c := &anthropic.Client{APIKey: "ak-test", BaseURL: ts.URL}
	s, err := c.Send(context.Background(), provider.Request{Model: "claude-test"})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	defer s.Close()

	_, err = s.Next()
	var apiErr *anthropic.APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("Next err = %v (%T), want *anthropic.APIError", err, err)
	}
	if apiErr.Type != "overloaded_error" {
		t.Errorf("Type = %q, want overloaded_error", apiErr.Type)
	}
}

// TestSend_SplitsSystemMessageOutOfMessagesArray proves the Messages API
// quirk this adapter exists to paper over: a "system"-role message must
// not appear in the request's messages array (Anthropic rejects it there)
// — it belongs in a separate top-level "system" field.
func TestSend_SplitsSystemMessageOutOfMessagesArray(t *testing.T) {
	var gotBody map[string]any
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decoding request body: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
	}))
	defer ts.Close()

	c := &anthropic.Client{APIKey: "ak-test", BaseURL: ts.URL}
	s, err := c.Send(context.Background(), provider.Request{
		Model: "claude-test",
		Messages: []provider.Message{
			{Role: "system", Content: "be terse"},
			{Role: "user", Content: "hi"},
		},
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	s.Close()

	if gotBody["system"] != "be terse" {
		t.Errorf(`request body "system" = %v, want "be terse"`, gotBody["system"])
	}
	msgs, ok := gotBody["messages"].([]any)
	if !ok || len(msgs) != 1 {
		t.Fatalf(`request body "messages" = %v, want exactly one non-system message`, gotBody["messages"])
	}
	first := msgs[0].(map[string]any)
	if first["role"] != "user" {
		t.Errorf(`messages[0]["role"] = %v, want "user" (system role must not appear here)`, first["role"])
	}
}
