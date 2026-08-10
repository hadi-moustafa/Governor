package proxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/hadi-moustafa/governor/internal/ledger"
	"github.com/hadi-moustafa/governor/internal/provider"
)

// runStream opens the upstream call and forwards chunks to w, reserving
// cost against the budget store as each chunk arrives. The moment a
// reservation is denied, it cancels ctx — the same context threaded into
// Provider.Send — so the upstream provider request is torn down, not just
// the client-facing stream. preflightMicros is the amount already reserved
// by ServeHTTP before this call, threaded through so reconciliation can
// see it.
func (h *Handler) runStream(w http.ResponseWriter, r *http.Request, req provider.Request, key string, preflightMicros int64) {
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	s, err := h.Provider.Send(ctx, req)
	if err != nil {
		if h.Metrics != nil {
			h.Metrics.StreamsErrored.Add(1)
		}
		h.logger().Warn("provider_send_failed", "key", key, "model", req.Model, "error", err)
		http.Error(w, fmt.Sprintf("provider error: %v", err), http.StatusBadGateway)
		h.reconcile(key, preflightMicros, 0, "provider_error")
		return
	}
	defer s.Close()

	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)

	var contentMicros int64
	for {
		chunk, err := s.Next()
		if err != nil {
			// io.EOF is a normal end; anything else (including a
			// provider-reported wire-level error, e.g.
			// *mockfinal.ProviderError, or the upstream connection dying
			// for some other reason) is not.
			finishReason := "stop"
			if !errors.Is(err, io.EOF) {
				finishReason = "provider_error"
				if h.Metrics != nil {
					h.Metrics.StreamsErrored.Add(1)
				}
				h.logger().Warn("stream_error", "key", key, "model", req.Model, "error", err)
			} else if h.Metrics != nil {
				h.Metrics.StreamsCompleted.Add(1)
			}
			h.reconcile(key, preflightMicros, contentMicros, finishReason)
			return
		}

		cost := h.Pricing.CostMicros(req.Model, chunk.Tokens)
		res, err := h.Store.Reserve(ctx, key, cost, h.CapMicros)
		if err != nil {
			h.reconcile(key, preflightMicros, contentMicros, "reserve_error")
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
			if h.Metrics != nil {
				h.Metrics.MidStreamCutoffs.Add(1)
			}
			h.logger().Warn("budget_mid_stream_cutoff",
				"key", key,
				"model", req.Model,
				"cap_micros", h.CapMicros,
				"content_micros", contentMicros,
				"denied_chunk_cost_micros", cost,
			)
			h.reconcile(key, preflightMicros, contentMicros, "budget_exceeded")
			return
		}

		contentMicros += cost
		writeChunk(w, flusher, chunk)
	}
}

// reconcile records a ledger.Record for the stream that just ended, and
// refunds any portion of preflightMicros that bought no real content — the
// case where the provider errored, or was canceled, before ever streaming
// a chunk (contentMicros == 0). It runs synchronously, in the same
// goroutine as the request, rather than backgrounded: this codebase's
// existing concurrency proofs are all about budget.Store's atomicity, and
// keeping reconciliation synchronous avoids introducing a second, separate
// class of race to reason about, at the cost of a little latency after the
// client-facing stream has already closed.
//
// It uses a short-lived context.Background(), not ctx from runStream:
// ctx is already canceled by the time this runs on the budget_exceeded
// path (reconcile is called after cancel()), and both budget.Store.Refund
// and ledger.Store.Record need a live context to do their own work.
func (h *Handler) reconcile(key string, preflightMicros, contentMicros int64, finishReason string) {
	if h.Ledger == nil {
		return
	}

	estimatedMicros := preflightMicros + contentMicros
	actualMicros := estimatedMicros
	var refundMicros int64
	if contentMicros == 0 {
		// Nothing was ever delivered, so the preflight reservation bought
		// no real content — the whole thing was an over-reservation.
		actualMicros = 0
		refundMicros = preflightMicros
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_ = h.Ledger.Record(ctx, ledger.Record{
		Key:             key,
		EstimatedMicros: estimatedMicros,
		ActualMicros:    actualMicros,
		FinishReason:    finishReason,
		RecordedAt:      time.Now(),
	})
	if h.Metrics != nil {
		h.Metrics.Reconciliations.Add(1)
	}

	if refundMicros > 0 {
		_ = h.Store.Refund(ctx, key, refundMicros)
		if h.Metrics != nil {
			h.Metrics.RefundsIssued.Add(1)
			h.Metrics.DriftMicrosTotal.Add(refundMicros)
		}
		h.logger().Info("reconciled",
			"key", key,
			"finish_reason", finishReason,
			"estimated_micros", estimatedMicros,
			"actual_micros", actualMicros,
			"refund_micros", refundMicros,
		)
	}
}

func writeChunk(w http.ResponseWriter, flusher http.Flusher, c provider.Chunk) {
	fmt.Fprintf(w, "data: {\"delta\":%q,\"tokens\":%d,\"finish_reason\":%q}\n\n", c.Delta, c.Tokens, c.FinishReason)
	if flusher != nil {
		flusher.Flush()
	}
}
