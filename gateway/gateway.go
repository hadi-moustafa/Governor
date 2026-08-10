// Package gateway is Governor's public API surface: gateway.New(cfg)
// returns a ready-to-serve http.Handler wired up exactly the way
// cmd/governor does — provider selection, budget enforcement, pricing,
// ledger reconciliation, and metrics — without requiring the caller to
// know about (or be able to import) any of the internal/... packages that
// actually implement it. This is the seam that makes Governor usable as
// an imported library, not just a standalone binary: `go get` this
// module, call gateway.New, mount the result on an http.Server.
package gateway

import (
	"context"
	"fmt"
	"net/http"

	"github.com/redis/go-redis/v9"

	"github.com/hadi-moustafa/governor/internal/budget"
	"github.com/hadi-moustafa/governor/internal/ledger"
	"github.com/hadi-moustafa/governor/internal/metrics"
	"github.com/hadi-moustafa/governor/internal/pricing"
	"github.com/hadi-moustafa/governor/internal/provider"
	"github.com/hadi-moustafa/governor/internal/provider/anthropic"
	"github.com/hadi-moustafa/governor/internal/provider/mock"
	"github.com/hadi-moustafa/governor/internal/provider/openai"
	"github.com/hadi-moustafa/governor/internal/proxy"
)

// Config configures a Gateway. Every field has a sensible zero-value
// default suitable for local development against the mock provider with
// no external services — the zero Config is valid and runs.
type Config struct {
	// Provider selects the backend: "mock" (the default), "openai", or
	// "anthropic".
	Provider string
	// ProviderURL is the mock provider's base URL. Ignored for real
	// providers, which have a fixed API endpoint.
	ProviderURL string

	// OpenAIAPIKey and AnthropicAPIKey authenticate the real provider
	// adapters. Required if Provider selects that backend.
	OpenAIAPIKey    string
	AnthropicAPIKey string

	// CapDollars is the hard spend cap, in dollars. Defaults to $1.00 if
	// zero.
	CapDollars float64
	// PreflightTokens is a crude fixed estimate reserved before the
	// provider is called at all. Defaults to 1 if zero.
	PreflightTokens int
	// MicrosPerToken prices the mock provider's fake tokens. Ignored for
	// real providers, which use an internal per-model pricing table.
	// Defaults to 1000 if zero.
	MicrosPerToken int64

	// RedisAddr, if set, backs the budget cap with Redis instead of the
	// default in-process store — use this if more than one Gateway
	// instance needs to agree on one running total.
	RedisAddr string
	// PostgresDSN, if set, persists reconciliation records to Postgres
	// instead of the default in-process ledger.
	PostgresDSN string
}

func (c Config) withDefaults() Config {
	if c.Provider == "" {
		c.Provider = "mock"
	}
	if c.ProviderURL == "" {
		c.ProviderURL = "http://localhost:8081"
	}
	if c.CapDollars == 0 {
		c.CapDollars = 1.00
	}
	if c.PreflightTokens == 0 {
		c.PreflightTokens = 1
	}
	if c.MicrosPerToken == 0 {
		c.MicrosPerToken = 1000
	}
	return c
}

// Gateway is a ready-to-serve http.Handler plus the metrics counters
// backing it — kept alongside the Handler, rather than only reachable
// through it, so a caller embedding Gateway in their own process can read
// Metrics.Snapshot() directly without an extra HTTP round trip to
// /debug/metrics.
type Gateway struct {
	http.Handler
	Metrics *metrics.Counters
}

// New builds a Gateway from cfg. It's the whole public surface: everything
// else (provider adapters, the budget store, the pricing table, the
// ledger, metrics) is wired together internally, matching exactly what
// cmd/governor's binary does — this function exists so that wiring is
// available as a library call, not just as `go run ./cmd/governor`.
func New(cfg Config) (*Gateway, error) {
	cfg = cfg.withDefaults()

	p, pricer, err := buildProvider(cfg)
	if err != nil {
		return nil, err
	}

	store, err := buildBudgetStore(cfg)
	if err != nil {
		return nil, err
	}

	led, err := buildLedgerStore(cfg)
	if err != nil {
		return nil, err
	}

	m := &metrics.Counters{}
	h := &proxy.Handler{
		Provider:        p,
		Store:           store,
		Pricing:         pricer,
		Ledger:          led,
		Metrics:         m,
		CapMicros:       int64(cfg.CapDollars * 1_000_000),
		PreflightTokens: cfg.PreflightTokens,
	}

	mux := http.NewServeMux()
	mux.Handle("/", h)
	mux.Handle("/debug/metrics", metrics.Handler(m))

	return &Gateway{Handler: mux, Metrics: m}, nil
}

// buildProvider selects the provider.Provider and matching pricing.Pricer
// for cfg.Provider. The mock provider keeps the flat-rate pricing.Model —
// its fake token counts don't correspond to any real model, so a
// per-model table has nothing meaningful to look up there.
func buildProvider(cfg Config) (provider.Provider, pricing.Pricer, error) {
	switch cfg.Provider {
	case "mock":
		return mock.New(cfg.ProviderURL), pricing.Model{MicrosPerToken: cfg.MicrosPerToken}, nil
	case "openai":
		if cfg.OpenAIAPIKey == "" {
			return nil, nil, fmt.Errorf("gateway: Provider=openai requires OpenAIAPIKey")
		}
		return openai.New(cfg.OpenAIAPIKey), pricing.Snapshot20260810, nil
	case "anthropic":
		if cfg.AnthropicAPIKey == "" {
			return nil, nil, fmt.Errorf("gateway: Provider=anthropic requires AnthropicAPIKey")
		}
		return anthropic.New(cfg.AnthropicAPIKey), pricing.Snapshot20260810, nil
	default:
		return nil, nil, fmt.Errorf("gateway: unknown Provider %q (want mock, openai, or anthropic)", cfg.Provider)
	}
}

func buildBudgetStore(cfg Config) (budget.Store, error) {
	if cfg.RedisAddr == "" {
		return budget.NewMemoryStore(), nil
	}
	return budget.NewRedisStore(redis.NewClient(&redis.Options{Addr: cfg.RedisAddr})), nil
}

func buildLedgerStore(cfg Config) (ledger.Store, error) {
	if cfg.PostgresDSN == "" {
		return ledger.NewMemoryStore(), nil
	}
	pg, err := ledger.NewPostgresStore(context.Background(), cfg.PostgresDSN)
	if err != nil {
		return nil, fmt.Errorf("gateway: connect to postgres: %w", err)
	}
	return pg, nil
}
