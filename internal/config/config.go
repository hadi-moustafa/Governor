// Package config loads Governor's runtime configuration from environment
// variables — API keys, budget caps, and the connection strings for the
// optional Redis/Postgres backends. It's deliberately env-var-only for
// now (no config file yet): a single Config struct is the seam a file
// loader could fill in later without reshaping callers, but there's no
// file format decision to make until something other than local/CLI use
// actually needs one.
package config

import (
	"os"
	"strconv"
)

// Config is Governor's runtime configuration. Every field has a sensible
// zero-value default suitable for local development against the mock
// provider with no external services.
type Config struct {
	// Addr is the gateway's listen address.
	Addr string

	// Provider selects which provider.Provider backs the gateway: "mock",
	// "openai", or "anthropic".
	Provider string
	// ProviderURL is the mock provider's base URL. Ignored for real
	// providers, which have a fixed API endpoint.
	ProviderURL string

	// OpenAIAPIKey and AnthropicAPIKey authenticate the real provider
	// adapters. Empty unless Provider selects that backend.
	OpenAIAPIKey    string
	AnthropicAPIKey string

	// CapMicros is the hard spend cap, in micros (1 unit = $0.000001).
	CapMicros int64
	// PreflightTokens is the crude fixed estimate reserved before the
	// provider is called at all.
	PreflightTokens int
	// MicrosPerToken prices the flat-rate pricing stub. Stands in until
	// a real per-model pricing table replaces it.
	MicrosPerToken int64

	// RedisAddr, if set, selects budget.RedisStore over the default
	// budget.MemoryStore.
	RedisAddr string
	// PostgresDSN, if set, selects ledger.PostgresStore over the default
	// ledger.MemoryStore.
	PostgresDSN string
}

// defaults returns a Config with every field set to its local-dev value.
func defaults() Config {
	return Config{
		Addr:            ":8080",
		Provider:        "mock",
		ProviderURL:     "http://localhost:8081",
		CapMicros:       1_000_000, // $1.00
		PreflightTokens: 1,
		MicrosPerToken:  1000,
	}
}

// Load reads configuration from environment variables, falling back to
// defaults() for anything unset. Every variable is prefixed GOVERNOR_ so
// it can't collide with an unrelated env var of the same short name
// (e.g. a shell's own ADDR).
func Load() (Config, error) {
	cfg := defaults()

	if v := os.Getenv("GOVERNOR_ADDR"); v != "" {
		cfg.Addr = v
	}
	if v := os.Getenv("GOVERNOR_PROVIDER"); v != "" {
		cfg.Provider = v
	}
	if v := os.Getenv("GOVERNOR_PROVIDER_URL"); v != "" {
		cfg.ProviderURL = v
	}
	cfg.OpenAIAPIKey = os.Getenv("GOVERNOR_OPENAI_API_KEY")
	cfg.AnthropicAPIKey = os.Getenv("GOVERNOR_ANTHROPIC_API_KEY")
	cfg.RedisAddr = os.Getenv("GOVERNOR_REDIS_ADDR")
	cfg.PostgresDSN = os.Getenv("GOVERNOR_POSTGRES_DSN")

	if v := os.Getenv("GOVERNOR_CAP_DOLLARS"); v != "" {
		dollars, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return Config{}, &ParseError{Var: "GOVERNOR_CAP_DOLLARS", Value: v, Err: err}
		}
		cfg.CapMicros = int64(dollars * 1_000_000)
	}
	if v := os.Getenv("GOVERNOR_PREFLIGHT_TOKENS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return Config{}, &ParseError{Var: "GOVERNOR_PREFLIGHT_TOKENS", Value: v, Err: err}
		}
		cfg.PreflightTokens = n
	}
	if v := os.Getenv("GOVERNOR_MICROS_PER_TOKEN"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return Config{}, &ParseError{Var: "GOVERNOR_MICROS_PER_TOKEN", Value: v, Err: err}
		}
		cfg.MicrosPerToken = n
	}

	return cfg, nil
}

// ParseError reports a malformed environment variable value.
type ParseError struct {
	Var   string
	Value string
	Err   error
}

func (e *ParseError) Error() string {
	return "config: invalid " + e.Var + "=" + e.Value + ": " + e.Err.Error()
}

func (e *ParseError) Unwrap() error { return e.Err }
