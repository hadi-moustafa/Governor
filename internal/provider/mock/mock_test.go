package mock_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hadi-moustafa/governor/internal/provider"
	"github.com/hadi-moustafa/governor/internal/provider/mock"
)

func TestSend_ParsesChunksAndDone(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"delta\":\"hello\",\"tokens\":3,\"finish_reason\":\"\"}\n\n")
		fmt.Fprint(w, "\n") // stray blank line should be skipped
		fmt.Fprint(w, "data: {\"delta\":\" world\",\"tokens\":2,\"finish_reason\":\"\"}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer ts.Close()

	c := mock.New(ts.URL)
	s, err := c.Send(context.Background(), provider.Request{Model: "fake", Messages: []provider.Message{{Role: "user", Content: "hi"}}})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	defer s.Close()

	chunk1, err := s.Next()
	if err != nil {
		t.Fatalf("Next (1): %v", err)
	}
	if chunk1.Delta != "hello" || chunk1.Tokens != 3 {
		t.Fatalf("chunk1 = %+v, want Delta=hello Tokens=3", chunk1)
	}

	chunk2, err := s.Next()
	if err != nil {
		t.Fatalf("Next (2): %v", err)
	}
	if chunk2.Delta != " world" || chunk2.Tokens != 2 {
		t.Fatalf("chunk2 = %+v, want Delta=' world' Tokens=2", chunk2)
	}

	_, err = s.Next()
	if err != io.EOF {
		t.Fatalf("Next (3) err = %v, want io.EOF", err)
	}
}

func TestSend_MalformedChunkReturnsError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {not valid json\n\n")
	}))
	defer ts.Close()

	c := mock.New(ts.URL)
	s, err := c.Send(context.Background(), provider.Request{Model: "fake"})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	defer s.Close()

	if _, err := s.Next(); err == nil {
		t.Fatal("expected an error decoding malformed chunk JSON, got nil")
	}
}

func TestSend_NonOKStatusIsError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusInternalServerError)
	}))
	defer ts.Close()

	c := mock.New(ts.URL)
	if _, err := c.Send(context.Background(), provider.Request{Model: "fake"}); err == nil {
		t.Fatal("expected an error for a non-200 response, got nil")
	}
}
