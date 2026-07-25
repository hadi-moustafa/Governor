package mockfinal_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hadi-moustafa/governor/internal/provider"
	"github.com/hadi-moustafa/governor/internal/provider/mockfinal"
)

func TestSend_SummaryOnlyReportsZeroThenTotal(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"delta\":\"chunk-0\",\"tokens\":0,\"finish_reason\":\"\"}\n\n")
		fmt.Fprint(w, "data: {\"delta\":\"chunk-1\",\"tokens\":0,\"finish_reason\":\"\"}\n\n")
		fmt.Fprint(w, "data: {\"delta\":\"chunk-2\",\"tokens\":21,\"finish_reason\":\"\"}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer ts.Close()

	c := mockfinal.New(ts.URL)
	s, err := c.Send(context.Background(), provider.Request{Model: "fake"})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	defer s.Close()

	for i := 0; i < 2; i++ {
		chunk, err := s.Next()
		if err != nil {
			t.Fatalf("Next (%d): %v", i, err)
		}
		if chunk.Tokens != 0 {
			t.Fatalf("chunk %d Tokens = %d, want 0 (usage not known yet)", i, chunk.Tokens)
		}
	}

	last, err := s.Next()
	if err != nil {
		t.Fatalf("Next (last): %v", err)
	}
	if last.Tokens != 21 {
		t.Fatalf("last chunk Tokens = %d, want 21 (cumulative total)", last.Tokens)
	}

	if _, err := s.Next(); err != io.EOF {
		t.Fatalf("Next (after last) err = %v, want io.EOF", err)
	}
}

func TestSend_ErrorEventReturnsProviderError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"error":{"type":"rate_limited","message":"slow down"}}`+"\n\n")
	}))
	defer ts.Close()

	c := mockfinal.New(ts.URL)
	s, err := c.Send(context.Background(), provider.Request{Model: "fake"})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	defer s.Close()

	_, err = s.Next()
	var perr *mockfinal.ProviderError
	if !errors.As(err, &perr) {
		t.Fatalf("Next err = %v (%T), want *mockfinal.ProviderError", err, err)
	}
	if perr.Type != "rate_limited" || perr.Message != "slow down" {
		t.Fatalf("ProviderError = %+v, want Type=rate_limited Message='slow down'", perr)
	}
}
