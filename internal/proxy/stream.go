package proxy

import (
	"context"
	"fmt"
	"net/http"

	"github.com/hadi-moustafa/governor/internal/provider"
)

// runStream opens the upstream call and forwards chunks to w, reserving
// cost against the budget store as each chunk arrives. The moment a
// reservation is denied, it cancels ctx — the same context threaded into
// Provider.Send — so the upstream provider request is torn down, not just
// the client-facing stream.
func (h *Handler) runStream(w http.ResponseWriter, r *http.Request, req provider.Request) {
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	s, err := h.Provider.Send(ctx, req)
	if err != nil {
		http.Error(w, fmt.Sprintf("provider error: %v", err), http.StatusBadGateway)
		return
	}
	defer s.Close()

	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)

	for {
		chunk, err := s.Next()
		if err != nil {
			// io.EOF (normal end) or the upstream connection died for some
			// other reason — either way there's nothing left to forward.
			return
		}

		res, err := h.Store.Reserve(ctx, placeholderKey, h.Pricing.CostMicros(chunk.Tokens), h.CapMicros)
		if err != nil {
			return
		}
		if !res.Allowed {
			// THE line: cancel is the same context.CancelFunc whose ctx
			// was passed into Provider.Send above, so this reaches the
			// actual upstream HTTP request, not just this loop. The chunk
			// that tripped the cap is never forwarded — a denied
			// reservation must never reach the client, mirroring
			// budget.Store.Reserve's own contract that it must never
			// reach a provider.
			cancel()
			writeChunk(w, flusher, provider.Chunk{FinishReason: "budget_exceeded"})
			return
		}

		writeChunk(w, flusher, chunk)
	}
}

func writeChunk(w http.ResponseWriter, flusher http.Flusher, c provider.Chunk) {
	fmt.Fprintf(w, "data: {\"delta\":%q,\"tokens\":%d,\"finish_reason\":%q}\n\n", c.Delta, c.Tokens, c.FinishReason)
	if flusher != nil {
		flusher.Flush()
	}
}
