// Command governor runs the gateway server: accepts a request, forwards it
// to a provider, streams the response back, and enforces a hard spend cap
// live — canceling the upstream provider request the instant the cap is
// crossed mid-stream. It's a thin CLI/env-var wrapper around the
// gateway package — the same wiring is available as a library call via
// gateway.New for anyone importing this module directly.
package main

import (
	"flag"
	"log"
	"net/http"

	"github.com/hadi-moustafa/governor/gateway"
	"github.com/hadi-moustafa/governor/internal/config"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatal(err)
	}

	// Flags override env-loaded config at invocation; env vars (or the
	// zero-config local-dev defaults) supply the flag defaults so
	// `governor` with no flags and no env still runs against the mock
	// provider out of the box.
	addr := flag.String("addr", cfg.Addr, "listen address")
	providerURL := flag.String("provider-url", cfg.ProviderURL, "upstream provider base URL (mock provider only)")
	capDollars := flag.Float64("cap", float64(cfg.CapMicros)/1_000_000, "hard spend cap in dollars")
	microsPerToken := flag.Int64("micros-per-token", cfg.MicrosPerToken, "fake price per token, in micros")
	flag.Parse()

	gw, err := gateway.New(gateway.Config{
		Provider:        cfg.Provider,
		ProviderURL:     *providerURL,
		OpenAIAPIKey:    cfg.OpenAIAPIKey,
		AnthropicAPIKey: cfg.AnthropicAPIKey,
		CapDollars:      *capDollars,
		PreflightTokens: cfg.PreflightTokens,
		MicrosPerToken:  *microsPerToken,
		RedisAddr:       cfg.RedisAddr,
		PostgresDSN:     cfg.PostgresDSN,
	})
	if err != nil {
		log.Fatal(err)
	}

	log.Printf("governor listening on %s, provider=%s, cap $%.2f", *addr, cfg.Provider, *capDollars)
	log.Fatal(http.ListenAndServe(*addr, gw))
}
