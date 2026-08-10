// Command example shows the whole point of gateway.New: import this
// module, wire up a spend-capped LLM gateway, and serve it — no more than
// this. Run a mock provider alongside it (`go run ./cmd/mockprovider`)
// and this is a complete, working, budget-enforced gateway.
package main

import (
	"log"
	"net/http"

	"github.com/hadi-moustafa/governor/gateway"
)

func main() {
	gw, err := gateway.New(gateway.Config{
		Provider:    "mock",
		ProviderURL: "http://localhost:8081",
		CapDollars:  1.00,
	})
	if err != nil {
		log.Fatal(err)
	}

	log.Fatal(http.ListenAndServe(":8080", gw))
}
