// Command governor runs the gateway server: accepts a request, forwards it
// to a provider, streams the response back, and enforces a hard spend cap
// live — canceling the upstream provider request the instant the cap is
// crossed mid-stream.
package main

import (
	"flag"
	"log"
	"net/http"

	"github.com/hadi-moustafa/governor/internal/budget"
	"github.com/hadi-moustafa/governor/internal/ledger"
	"github.com/hadi-moustafa/governor/internal/pricing"
	"github.com/hadi-moustafa/governor/internal/provider/mock"
	"github.com/hadi-moustafa/governor/internal/proxy"
)

func main() {
	addr := flag.String("addr", ":8080", "listen address")
	providerURL := flag.String("provider-url", "http://localhost:8081", "upstream provider base URL")
	capDollars := flag.Float64("cap", 1.00, "hard spend cap in dollars")
	microsPerToken := flag.Int64("micros-per-token", 1000, "fake price per token, in micros")
	flag.Parse()

	h := &proxy.Handler{
		Provider:        mock.New(*providerURL),
		Store:           budget.NewMemoryStore(),
		Pricing:         pricing.Model{MicrosPerToken: *microsPerToken},
		Ledger:          ledger.NewMemoryStore(),
		CapMicros:       int64(*capDollars * 1_000_000),
		PreflightTokens: 1,
	}

	log.Printf("governor listening on %s, forwarding to %s, cap $%.2f", *addr, *providerURL, *capDollars)
	log.Fatal(http.ListenAndServe(*addr, h))
}
