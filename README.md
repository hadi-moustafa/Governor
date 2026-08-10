# Governor

A lightweight LLM gateway in Go: routes requests to LLM providers, enforces
hard spend caps with atomic pre-flight checks, and meters cost *live* during
streaming responses — cutting a request off mid-stream the instant it would
cross the cap, not after the bill arrives.

Wire an LLM into your app and you lose two things: visibility into what it's
costing you in real time, and the ability to actually stop it before you
overspend. Governor exists to give both back, in one binary, with zero
required infrastructure.

## Quickstart

**Try it against the mock provider — no API keys needed:**

```sh
go run ./cmd/mockprovider &
go run ./cmd/governor
curl -X POST localhost:8080 \
  -d '{"model":"fake","messages":[{"role":"user","content":"hi"}]}'
```

**Point it at a real provider:**

```sh
GOVERNOR_PROVIDER=openai \
GOVERNOR_OPENAI_API_KEY=sk-... \
GOVERNOR_CAP_DOLLARS=5.00 \
  go run ./cmd/governor
```

**As an importable library** (`go get github.com/hadi-moustafa/governor`):

```go
gw, err := gateway.New(gateway.Config{
    Provider:     "openai",
    OpenAIAPIKey: os.Getenv("OPENAI_API_KEY"),
    CapDollars:   5.00,
})
if err != nil {
    log.Fatal(err)
}
http.ListenAndServe(":8080", gw)
```

See [`example/`](example) for a complete, runnable version.

**Terminal UI** — configure a provider and cap on-screen, start the gateway,
watch live usage update:

```sh
go run ./cmd/governorctl
```

**Docker:**

```sh
docker build -t governor .
docker run -p 8080:8080 -e GOVERNOR_PROVIDER=mock governor
```

## How the cap actually works

The hard part isn't rejecting a request before it starts — it's stopping
one that's already streaming, before the last few tokens push you over.
Governor does both:

1. **Pre-flight**: before a request ever reaches a provider, a small
   estimated cost is atomically reserved against the cap. If that alone
   would exceed it, the request is denied — the provider is never called,
   so no tokens are bought for nothing.
2. **Live per-chunk metering**: as the provider streams a response back,
   each chunk's real cost is reserved against the same cap the instant
   it's known. The moment a reservation would be denied, Governor cancels
   the *upstream* provider request — not just the client-facing stream —
   so generation actually stops, not just delivery to the client.
3. **Reconciliation**: once a stream ends (normally, cut off, or errored),
   estimated and actual cost are compared. Any leftover reservation that
   bought no real content — e.g. a provider that failed immediately — is
   refunded back to the cap.

```mermaid
sequenceDiagram
    participant Client
    participant Governor as Governor (proxy.Handler)
    participant Budget as budget.Store
    participant Provider as LLM Provider
    participant Ledger as ledger.Store

    Client->>Governor: POST request
    Governor->>Budget: Reserve(preflight estimate)
    alt over cap
        Budget-->>Governor: denied
        Governor-->>Client: 429, provider never called
    else allowed
        Budget-->>Governor: allowed
        Governor->>Provider: Send (streaming)
        loop each chunk
            Provider-->>Governor: chunk (delta, tokens)
            Governor->>Budget: Reserve(chunk cost)
            alt over cap
                Budget-->>Governor: denied
                Governor->>Provider: cancel (upstream request torn down)
                Governor-->>Client: budget_exceeded event
            else allowed
                Governor-->>Client: forward chunk
            end
        end
        Governor->>Ledger: Record(estimated vs actual)
        opt over-reservation found
            Governor->>Budget: Refund(delta)
        end
    end
```

One real, unresolved limitation worth knowing: a provider that only reports
usage on its *final* chunk (this is exactly how OpenAI's
`stream_options.include_usage` behaves) makes true mid-stream cutoff
impossible in principle — cost isn't knowable until the stream is already
over. Anthropic's `message_delta` events report output tokens
incrementally, so live cutoff works as designed there. See
[`internal/provider/mockfinal`](internal/provider/mockfinal) — a fake
provider built specifically to reproduce and pin down this gap with a
regression test — and [`internal/provider/openai`](internal/provider/openai),
which confirms it's not hypothetical: that's what the real API does.

## Project layout

```text
/cmd/governor        - gateway server binary
/cmd/governorctl      - bubbletea/lipgloss TUI: configure, start/stop, live usage
/cmd/mockprovider     - fake SSE-streaming LLM server, zero API cost, used for all dev/testing
/gateway              - public library API: gateway.New(cfg) -> ready-to-serve http.Handler
/example              - minimal runnable example importing the gateway package
/internal/proxy       - forwards requests to a provider, streams response, meters cost live
/internal/budget      - atomic reserve/decrement of spend caps (in-memory default, Redis-backed optional)
/internal/pricing     - flat-rate stub + a real versioned per-model pricing table
/internal/provider    - Provider interface + adapters (mock, mockfinal, openai, anthropic)
/internal/ledger      - durable reconciled cost records (in-memory default, Postgres-backed optional)
/internal/metrics     - in-process counters (preflight denials, cutoffs, drift) + /debug/metrics
/internal/config      - environment-variable configuration loading
```

## Configuration reference

Every variable is optional; the zero-config defaults run against the mock
provider with no external services.

| Variable | Default | Purpose |
| --- | --- | --- |
| `GOVERNOR_ADDR` | `:8080` | Listen address |
| `GOVERNOR_PROVIDER` | `mock` | `mock`, `openai`, or `anthropic` |
| `GOVERNOR_PROVIDER_URL` | `http://localhost:8081` | Mock provider's base URL (ignored for real providers) |
| `GOVERNOR_OPENAI_API_KEY` | — | Required if `GOVERNOR_PROVIDER=openai` |
| `GOVERNOR_ANTHROPIC_API_KEY` | — | Required if `GOVERNOR_PROVIDER=anthropic` |
| `GOVERNOR_CAP_DOLLARS` | `1.00` | Hard spend cap, in dollars |
| `GOVERNOR_PREFLIGHT_TOKENS` | `1` | Tokens reserved before a provider is called at all |
| `GOVERNOR_MICROS_PER_TOKEN` | `1000` | Flat-rate price for the mock provider only |
| `GOVERNOR_REDIS_ADDR` | — | If set, backs the budget cap with Redis instead of an in-process store |
| `GOVERNOR_POSTGRES_DSN` | — | If set, persists reconciliation records to Postgres instead of in-memory |

Real providers use an internal versioned per-model pricing table instead
of the flat rate — see [`internal/pricing`](internal/pricing).

`docker-compose.yml` at the repo root starts local Redis and Postgres for
exercising either opt-in backend:

```sh
docker compose up -d
GOVERNOR_REDIS_ADDR=localhost:6379 GOVERNOR_POSTGRES_DSN="postgres://governor:governor@localhost:5432/governor" \
  go run ./cmd/governor
```

## Development

```sh
go build ./...
go vet ./...
go test -race ./...
```

Redis/Postgres integration tests self-skip unless
`GOVERNOR_TEST_REDIS_ADDR` / `GOVERNOR_TEST_POSTGRES_DSN` are set, so the
default test run needs nothing beyond Go itself.

## Status

Phases 1 and 2 are done: the core engine (budget enforcement, live
metering, mid-stream cutoff, reconciliation) against a mocked provider,
plus real OpenAI/Anthropic adapters, a real pricing table, structured
logging/metrics, library packaging, and the `governorctl` TUI. Phase 3
(this work: containerize, CI, distributable install, docs) is in
progress. See [CLAUDE.md](CLAUDE.md) for the detailed running log of
decisions and progress.

## License

[MIT](LICENSE)
