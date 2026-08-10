package openai_test

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
	"github.com/hadi-moustafa/governor/internal/provider/openai"
)

// TestSend_ParsesContentThenFinalUsageChunk mirrors the real shape OpenAI
// documents for stream_options.include_usage=true: content chunks report
// Tokens:0 (no usage field at all), and one final chunk with an empty
// choices array carries the real cumulative token count. This is the
// live-provider confirmation of the finding internal/provider/mockfinal's
// SummaryOnly mode was built to anticipate.
func TestSend_ParsesContentThenFinalUsageChunk(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer sk-test" {
			t.Errorf("Authorization header = %q, want %q", got, "Bearer sk-test")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"Hel"},"finish_reason":null}]}`+"\n\n")
		fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"lo"},"finish_reason":null}]}`+"\n\n")
		fmt.Fprint(w, `data: {"choices":[{"delta":{},"finish_reason":"stop"}]}`+"\n\n")
		fmt.Fprint(w, `data: {"choices":[],"usage":{"total_tokens":42}}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer ts.Close()

	c := &openai.Client{APIKey: "sk-test", BaseURL: ts.URL}
	s, err := c.Send(context.Background(), provider.Request{Model: "gpt-test", Messages: []provider.Message{{Role: "user", Content: "hi"}}})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	defer s.Close()

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

	chunk3, err := s.Next()
	if err != nil {
		t.Fatalf("Next (3): %v", err)
	}
	if chunk3.FinishReason != "stop" || chunk3.Tokens != 0 {
		t.Fatalf("chunk3 = %+v, want FinishReason=stop Tokens=0", chunk3)
	}

	usageChunk, err := s.Next()
	if err != nil {
		t.Fatalf("Next (usage): %v", err)
	}
	if usageChunk.Delta != "" || usageChunk.Tokens != 42 {
		t.Fatalf("usage chunk = %+v, want empty Delta and Tokens=42", usageChunk)
	}

	if _, err := s.Next(); err != io.EOF {
		t.Fatalf("Next (after DONE) err = %v, want io.EOF", err)
	}
}

func TestSend_ErrorStatusReturnsTypedAPIError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"error":{"type":"invalid_request_error","message":"Incorrect API key provided.","code":"invalid_api_key"}}`)
	}))
	defer ts.Close()

	c := &openai.Client{APIKey: "sk-bad", BaseURL: ts.URL}
	_, err := c.Send(context.Background(), provider.Request{Model: "gpt-test"})
	if err == nil {
		t.Fatal("expected an error for a 401 response")
	}
	var apiErr *openai.APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("err = %v (%T), want *openai.APIError", err, err)
	}
	if apiErr.StatusCode != http.StatusUnauthorized {
		t.Errorf("StatusCode = %d, want 401", apiErr.StatusCode)
	}
	if apiErr.Code != "invalid_api_key" {
		t.Errorf("Code = %q, want invalid_api_key", apiErr.Code)
	}
}

func TestSend_RequestBodyIncludesStreamOptions(t *testing.T) {
	var gotBody map[string]any
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decoding request body: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer ts.Close()

	c := &openai.Client{APIKey: "sk-test", BaseURL: ts.URL}
	s, err := c.Send(context.Background(), provider.Request{Model: "gpt-test", Messages: []provider.Message{{Role: "user", Content: "hi"}}})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	defer s.Close()
	if _, err := s.Next(); err != io.EOF {
		t.Fatalf("Next err = %v, want io.EOF", err)
	}

	if gotBody["stream"] != true {
		t.Errorf(`request body "stream" = %v, want true`, gotBody["stream"])
	}
	streamOpts, ok := gotBody["stream_options"].(map[string]any)
	if !ok || streamOpts["include_usage"] != true {
		t.Errorf(`request body "stream_options.include_usage" = %v, want true`, gotBody["stream_options"])
	}
}
