// Package proxy forwards requests to a provider, streams the response back
// to the caller, and meters cost live — canceling the upstream provider
// request (not just the client-facing stream) the instant a budget cap is
// crossed.
package proxy

import (
	"encoding/json"
	"net/http"

	"github.com/hadi-moustafa/governor/internal/budget"
	"github.com/hadi-moustafa/governor/internal/pricing"
	"github.com/hadi-moustafa/governor/internal/provider"
)

// placeholderKey is the budget reservation key used until a real
// reservation key scheme exists tied to auth (still open — see CLAUDE.md).
const placeholderKey = "default"

// Handler forwards decoded requests to Provider, metering cost against
// Store as chunks arrive.
type Handler struct {
	Provider provider.Provider
	Store    budget.Store
	Pricing  pricing.Model

	// CapMicros is the hard spend cap for placeholderKey.
	CapMicros int64
	// PreflightTokens is a crude fixed estimate reserved before the
	// provider is called at all, standing in for a real prompt tokenizer.
	PreflightTokens int
}

var _ http.Handler = (*Handler)(nil)

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var req provider.Request
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	pre, err := h.Store.Reserve(r.Context(), placeholderKey, h.Pricing.CostMicros(h.PreflightTokens), h.CapMicros)
	if err != nil {
		http.Error(w, "budget check failed", http.StatusInternalServerError)
		return
	}
	if !pre.Allowed {
		// Denied before ever reaching the provider — no tokens bought.
		http.Error(w, "budget exceeded", http.StatusTooManyRequests)
		return
	}

	h.runStream(w, r, req)
}
