package config_test

import (
	"errors"
	"testing"

	"github.com/hadi-moustafa/governor/internal/config"
)

func TestLoad_DefaultsWithNoEnvSet(t *testing.T) {
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Addr != ":8080" {
		t.Errorf("Addr = %q, want :8080", cfg.Addr)
	}
	if cfg.Provider != "mock" {
		t.Errorf("Provider = %q, want mock", cfg.Provider)
	}
	if cfg.CapMicros != 1_000_000 {
		t.Errorf("CapMicros = %d, want 1000000", cfg.CapMicros)
	}
	if cfg.RedisAddr != "" || cfg.PostgresDSN != "" {
		t.Errorf("expected RedisAddr/PostgresDSN empty by default, got %q/%q", cfg.RedisAddr, cfg.PostgresDSN)
	}
}

func TestLoad_EnvOverridesDefaults(t *testing.T) {
	t.Setenv("GOVERNOR_ADDR", ":9090")
	t.Setenv("GOVERNOR_PROVIDER", "openai")
	t.Setenv("GOVERNOR_OPENAI_API_KEY", "sk-test")
	t.Setenv("GOVERNOR_ANTHROPIC_API_KEY", "ak-test")
	t.Setenv("GOVERNOR_CAP_DOLLARS", "2.50")
	t.Setenv("GOVERNOR_PREFLIGHT_TOKENS", "5")
	t.Setenv("GOVERNOR_MICROS_PER_TOKEN", "2000")
	t.Setenv("GOVERNOR_REDIS_ADDR", "localhost:6379")
	t.Setenv("GOVERNOR_POSTGRES_DSN", "postgres://x")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Addr != ":9090" {
		t.Errorf("Addr = %q, want :9090", cfg.Addr)
	}
	if cfg.Provider != "openai" {
		t.Errorf("Provider = %q, want openai", cfg.Provider)
	}
	if cfg.OpenAIAPIKey != "sk-test" {
		t.Errorf("OpenAIAPIKey = %q, want sk-test", cfg.OpenAIAPIKey)
	}
	if cfg.AnthropicAPIKey != "ak-test" {
		t.Errorf("AnthropicAPIKey = %q, want ak-test", cfg.AnthropicAPIKey)
	}
	if cfg.CapMicros != 2_500_000 {
		t.Errorf("CapMicros = %d, want 2500000", cfg.CapMicros)
	}
	if cfg.PreflightTokens != 5 {
		t.Errorf("PreflightTokens = %d, want 5", cfg.PreflightTokens)
	}
	if cfg.MicrosPerToken != 2000 {
		t.Errorf("MicrosPerToken = %d, want 2000", cfg.MicrosPerToken)
	}
	if cfg.RedisAddr != "localhost:6379" {
		t.Errorf("RedisAddr = %q, want localhost:6379", cfg.RedisAddr)
	}
	if cfg.PostgresDSN != "postgres://x" {
		t.Errorf("PostgresDSN = %q, want postgres://x", cfg.PostgresDSN)
	}
}

func TestLoad_MalformedCapDollarsReturnsParseError(t *testing.T) {
	t.Setenv("GOVERNOR_CAP_DOLLARS", "not-a-number")

	_, err := config.Load()
	if err == nil {
		t.Fatal("expected an error for a malformed GOVERNOR_CAP_DOLLARS")
	}
	var perr *config.ParseError
	if !errors.As(err, &perr) {
		t.Fatalf("err = %v (%T), want *config.ParseError", err, err)
	}
	if perr.Var != "GOVERNOR_CAP_DOLLARS" {
		t.Errorf("ParseError.Var = %q, want GOVERNOR_CAP_DOLLARS", perr.Var)
	}
}
