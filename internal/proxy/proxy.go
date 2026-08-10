// Package proxy forwards requests to a provider, streams the response back
// to the caller, and meters cost live — canceling the upstream provider
// request (not just the client-facing stream) the instant a budget cap is
// crossed.
package proxy

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/hadi-moustafa/governor/internal/budget"
	"github.com/hadi-moustafa/governor/internal/ledger"
	"github.com/hadi-moustafa/governor/internal/metrics"
	"github.com/hadi-moustafa/governor/internal/pricing"
	"github.com/hadi-moustafa/governor/internal/provider"
)

// keyHeader names the request header clients use to identify themselves for
// budget tracking. There's no auth yet (still open — see CLAUDE.md), so this
// isn't validated or trusted for anything beyond bucketing spend; it just
// turns the reservation key into a real input instead of a constant.
const keyHeader = "X-Governor-Key"

// defaultKey is the reservation key used when keyHeader is absent.
const defaultKey = "default"

// reservationKey resolves the budget reservation key for r. Callers should
// resolve it once per request and thread the same value through preflight,
// per-chunk metering, and reconciliation, rather than re-reading the header
// at each step.
func reservationKey(r *http.Request) string {
	if k := r.Header.Get(keyHeader); k != "" {
		return k
	}
	return defaultKey
}

// Handler forwards decoded requests to Provider, metering cost against
// Store as chunks arrive.
type Handler struct {
	Provider provider.Provider
	Store    budget.Store
	Pricing  pricing.Pricer
	// Ledger records estimated-vs-actual cost once a stream ends, and is
	// where any over-reservation gets refunded back to Store. Optional —
	// if nil, reconciliation is skipped (useful for tests that don't care
	// about it).
	Ledger ledger.Store
	// Metrics counts budget rejections, cutoffs, and reconciliation
	// drift. Optional — if nil, counting is skipped.
	Metrics *metrics.Counters
	// Logger receives structured events for the same three decision
	// points Metrics counts. Optional — defaults to slog.Default() if
	// nil, so callers get reasonable output for free.
	Logger *slog.Logger

	// CapMicros is the hard spend cap for a request's reservation key
	// (see reservationKey).
	CapMicros int64
	// PreflightTokens is a crude fixed estimate reserved before the
	// provider is called at all, standing in for a real prompt tokenizer.
	PreflightTokens int
}

var _ http.Handler = (*Handler)(nil)

func (h *Handler) logger() *slog.Logger {
	if h.Logger != nil {
		return h.Logger
	}
	return slog.Default()
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var req provider.Request
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	key := reservationKey(r)
	preflightMicros := h.Pricing.CostMicros(req.Model, h.PreflightTokens)

	pre, err := h.Store.Reserve(r.Context(), key, preflightMicros, h.CapMicros)
	if err != nil {
		http.Error(w, "budget check failed", http.StatusInternalServerError)
		return
	}
	if !pre.Allowed {
		// Denied before ever reaching the provider — no tokens bought.
		if h.Metrics != nil {
			h.Metrics.PreflightDenials.Add(1)
		}
		h.logger().Warn("budget_preflight_denied",
			"key", key,
			"model", req.Model,
			"cap_micros", h.CapMicros,
			"spent_micros", pre.SpentMicros,
			"preflight_micros", preflightMicros,
		)
		http.Error(w, "budget exceeded", http.StatusTooManyRequests)
		return
	}

	h.runStream(w, r, req, key, preflightMicros)
}
