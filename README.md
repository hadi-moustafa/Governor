# Governor

Building a lightweight LLM gateway in Go (learning Go as I go) — real-time
cost visibility and hard spend caps, without standing up LiteLLM/Helicone-
style infra.

## Why

Wire an LLM into your app and you lose two things: you can't see what it's
costing you, and you can't stop it. The bill shows up at the end of the
month like a verdict. Mature tools (LiteLLM, Helicone, Portkey) solve this,
but they're built for teams — standing up a proxy, Postgres, and Redis just
to answer "how much am I spending?" is a lot of machine for one honest
question.

Governor is meant to be a gateway you point your app at that does two
things well before anything else:

- shows you the real-time cost of every LLM call
- lets you set a hard cap that actually cuts a request off before you
  overspend

One command, no infrastructure to babysit.

## Status: early / work in progress

This is **not** a working gateway yet. `cmd/governor` is still a stub. What
exists and is tested today is the piece the rest of the project depends on:
atomic, race-safe enforcement of a spend cap.

### What works right now

[`internal/budget`](internal/budget) enforces a per-key spend cap behind a
small `Store` interface with two backends:

- **`MemoryStore`** (the default) — a mutex-guarded counter in the
  process's own memory. A single Governor instance needs nothing more
  than this to enforce its own cap correctly, so the common case — one
  binary, one process — pulls in zero external infrastructure.
- **`RedisStore`** (opt-in) — the same guarantee across multiple
  processes. The check-and-commit runs as one Lua script inside Redis
  (`EVAL`) instead of a client-side read-then-write, so it's immune to
  the race where two concurrent requests on two different instances both
  read the same "current spend" and both get allowed through. Reach for
  this only once you're running more than one Governor replica, or need
  the cap to survive a restart.

Both are O(1): the check doesn't get slower as spend history grows.

```go
store := budget.NewMemoryStore() // or budget.NewRedisStore(redisClient)

res, err := store.Reserve(ctx, apiKey, estimatedCostMicros, capMicros)
if !res.Allowed {
    // denied before the request ever reaches a provider — no tokens bought
    return ErrBudgetExceeded
}
```

Proven with a concurrency test against both backends, not just a manual
check: 200 goroutines fire simultaneous reservations against a $1.00 cap
in $0.05 increments, and exactly 20 are allowed — verified under
`go test -race`.

```
go test ./internal/budget/... -v -race
```

No Docker or local Redis required to run that — the Redis-backed tests use
an in-process Redis-protocol emulator, so the whole suite runs on a fresh
clone with just Go installed.

## Project layout

```
/cmd/governor      - gateway server binary
/cmd/governorctl    - CLI/TUI (bubbletea) for setup, budget caps, live usage
/cmd/mockprovider   - fake SSE-streaming LLM server, zero API cost, used for all dev/testing
/internal/proxy     - forwards requests to a provider, streams response, meters cost live
/internal/budget    - atomic reserve/decrement of spend caps (in-memory default, Redis-backed optional), pre-flight checks
/internal/provider  - Provider interface + adapters (mock, OpenAI, Anthropic, ...)
/internal/ledger    - durable reconciled cost records (Postgres), source of truth post-stream
```

## Roadmap

- **Phase 1** (in progress): core engine against a mocked provider only —
  end-to-end request passthrough, the budget store above, and live
  stream metering with mid-stream cutoff when a cap is hit.
- **Phase 2**: real OpenAI/Anthropic adapters, pricing config, packaging
  as an importable Go library, and a `governorctl` terminal UI.
- **Phase 3**: containerize, CI, hosted deploy, and a distributable
  install (`go install` / `curl | sh`).

See [CLAUDE.md](CLAUDE.md) for the detailed running log of decisions and
progress.

## License

TBD — planned MIT, not yet added.
