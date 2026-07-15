# GOVERNOR

A Go-based LLM gateway: routes requests to LLM providers, enforces spend
budgets with atomic pre-flight checks, meters cost live during streaming
responses (cutting off mid-stream if a cap is hit), and packages as both an
importable Go library and a TUI (`governorctl`) for setup/monitoring.

Module: `github.com/hadi-moustafa/governor`

This file is the running source of truth for the project: decisions, setup
steps, and the plan. Every entry in the Log section is dated. Update it
whenever something notable happens (setup step, design decision, bug fix,
plan change) — don't let it go stale.

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

## Plan

Organized as **phases**, not literal calendar days/weeks — there's no fixed
deadline, so "Day 3" etc. below just means sequencing/scope within a phase,
not a due date.

### Phase 1 — Core engine, local only, mocked provider

- Project skeleton + mock provider: tiny mock LLM server (`/cmd/mockprovider`)
  faking SSE streaming chunks with fake token counts. Deliverable: `go run
  ./cmd/governor` accepts a request, forwards to mock provider, streams
  response back untouched.
- Provider adapter abstraction: `Provider` interface (`Send(req) (stream,
  error)`), implemented once for the mock. **Refinement:** before moving to
  real providers, also build a second *fake* provider with deliberately
  different quirks (different usage-reporting shape, different error format)
  to stress-test the interface — avoids reshaping it later once OpenAI/
  Anthropic's real differences show up.
- Budget store: Docker Compose (Redis + Postgres) locally. Atomic
  reserve/decrement via Redis Lua script (avoids races under concurrent
  requests). Deliverable: pre-flight check rejects a request if estimated
  cost exceeds remaining budget. **Refinement:** this needs real concurrent
  integration tests (fire N parallel requests, assert no double-spend) —
  not just manual/one-shot verification, since correctness here is the whole
  point of the project.
- Live stream metering + mid-stream cutoff: interceptor tallies running cost
  per chunk, kills the connection when cap is hit. **Refinement:** cutoff
  must explicitly cancel the *upstream* provider request via
  `context.Context`, not just stop forwarding to the client — otherwise
  still paying for tokens after the client-facing stream stops. Reconciliation
  worker corrects the ledger with actual vs. estimated cost after a stream
  ends or is cut. Deliverable: prove — with the mock provider — a request
  gets cut off mid-stream when it exceeds a cap.

### Phase 2 — Real providers + library packaging + TUI shell

- Real provider adapters: OpenAI + Anthropic, small API credit ($5-10 each).
  Real auth headers, real SSE parsing (Anthropic `message_delta`, OpenAI
  `stream_options.include_usage`) for real token counts.
- Pricing table + config: versioned per-model/per-token pricing (config file
  or embedded JSON, since provider prices change). Config loading (env vars,
  config file) for API keys, caps, Redis/Postgres connection strings.
  **Refinement:** fold in minimal observability here too (structured logs +
  basic metrics on budget rejections, cutoffs, estimated-vs-actual cost
  drift) — this is close to core functionality for a spend-control gateway,
  not a nice-to-have to bolt on later.
- Package as an importable library: public API surface (e.g. `gateway.New
  (config)` returning an `http.Handler` or a `Client`). Minimal `example/`
  folder showing "import this, run this, done." Deliverable: someone else
  could `go get` the module and use it in ~10 lines.
- Terminal UI: `bubbletea` + `lipgloss` for `governorctl` — configure API
  keys, set budget caps, view live usage/ledger, start/stop the gateway
  daemon. Deliverable: `governorctl` launches a usable TUI, no manual config
  file editing required.

### Phase 3 — Deployment + distribution

*Treated as more flexible/exploratory than Phases 1-2 — goreleaser,
install.sh hosting, Fly.io/Railway + DNS, and secrets management each carry
first-time friction, so don't expect tight day-for-day precision here.*

- Containerize + CI: Dockerfile for the gateway server. GitHub Actions:
  build/test/lint on every push; release binaries for linux/mac/windows via
  goreleaser.
- Deploy the hosted piece: decide what's "hosted" vs. "local library" — the
  gateway server can run anywhere; if a public demo/setup UI is wanted,
  that's a small separate web app. Pick a host (Fly.io/Railway), get a
  domain (~$12/yr).
- Terminal install experience: `goreleaser` + GitHub Releases for
  `curl -sSL .../install.sh | sh`, or `go install .../cmd/governorctl@latest`.
- Docs, polish, launch: README with quickstart, architecture diagram
  (Mermaid), config reference, usage examples, MIT license. Soft-launch for
  first feedback.

**Money checkpoint:** Phase 1: $0. Phase 2: $5-10 in API credits. Phase 3:
~$12/yr domain + hosting (Fly.io/Railway free tiers likely cover a small demo).

## Log

### 2026-07-14
- Discussed and refined the 3-phase plan above (originally written as
  "weeks" — reinterpreted as phases, no calendar deadline). Agreed on the
  six refinements folded into the phase descriptions above (second fake
  provider to stress-test the interface, explicit upstream cancellation on
  cutoff, concurrency tests as first-class, observability moved into Phase
  2, Phase 3 treated as flexible).
- Installed Go 1.26.5 on Fedora via the official tarball from go.dev/dl
  (not `dnf install golang`, to avoid an older packaged version) to
  `/usr/local/go`. Added `/usr/local/go/bin` and `$(go env GOPATH)/bin` to
  `PATH` in `~/.bashrc`.
- Installed the VS Code Go extension (`golang.go`, Go Team at Google) and
  ran "Go: Install/Update Tools" (gopls, dlv, staticcheck, etc.).
- Ran `go mod init github.com/hadi-moustafa/governor`.
- Created the initial folder skeleton (see Project layout above) with
  placeholder `main.go`/package files — `go build ./...` and `go vet ./...`
  both pass clean. No real logic yet; Phase 1 Day 1-2 work (mock provider +
  end-to-end passthrough) hasn't started.
- `git init`'d the repo locally (default branch `master`). Added a
  Go-appropriate `.gitignore` (build artifacts, `.env`, and local tool
  config `.claude/`/`.directory` which don't belong in the repo).
- Connected the local repo to `git@github.com:hadi-moustafa/Governor.git`
  (SSH — an existing ed25519 key on this machine was already authorized
  with the `hadi-moustafa` GitHub account) and pushed `master` upstream.

### 2026-07-15
- Implemented the core of the Phase 1 budget store (`internal/budget`):
  atomic reserve/decrement of a per-key spend cap, backed by Redis. The
  design question driving this: how to enforce a hard cap the instant a
  request would cross it, without adding latency to every call before it.
  Answer landed on: do the check-and-increment as a single Lua script
  (`EVAL`) executed inside Redis, not a client-side GET-then-SET. That
  makes it one fixed-cost round trip per request (O(1) regardless of how
  much spend history exists — no scan, no lock contention on the client),
  and it's atomic because Redis serializes script execution, so two
  concurrent requests racing for the last bit of budget can't both read
  the same "current" value and both get allowed through. Amounts are
  tracked in micros (int64) to keep money out of floating point.
  `Store.Reserve(ctx, key, amountMicros, capMicros)` returns
  `Result{Allowed, SpentMicros}`; a denied reservation must never reach a
  provider. `Store.Refund` decrements the tally, for truing up an
  over-estimated reservation after actual usage is known (mid-stream
  cutoff, reconciliation).
- Added dependencies: `github.com/redis/go-redis/v9` (client) and
  `github.com/alicebob/miniredis/v2` (in-process Redis emulator with Lua
  support, dev-only — used in tests so the budget package is exercisable
  without standing up real Redis; this machine doesn't have docker group
  membership yet, so real Redis via `docker compose` is still untried —
  revisit once that's sorted).
- Test coverage for the atomicity claim: `TestReserve_ConcurrentNoDoubleSpend`
  fires 200 goroutines at a $1.00 cap in $0.05 increments and asserts
  exactly 20 are allowed — passes under `go test -race`. `BenchmarkReserve`
  confirms the check costs a flat ~95µs/op (in-process Redis emulator)
  with no growth as call count increases. `Example` in
  `internal/budget/example_test.go` is a runnable, `go test`-verified demo
  of the cutoff behavior end-to-end.
- Still open before this is genuinely Phase-1-done: wire `Reserve` into
  `cmd/governor`'s request path (it currently only exists as a
  standalone, tested package), add the `docker-compose.yml` for real
  Redis + Postgres, and decide the reservation key scheme (per API key?
  per project?) once auth exists.
- Caught a positioning contradiction before it shipped: the original
  pitch (README/first LinkedIn post) says Governor avoids the
  proxy+Postgres+Redis hassle of LiteLLM/Helicone-style tools, but the
  budget store as built only had a Redis-backed implementation — a solo
  dev running one process would've needed Redis just to enforce their own
  cap. Fixed by extracting a `Store` interface
  (`internal/budget/budget.go`) with two implementations instead of
  retracting the pitch: `MemoryStore` (`internal/budget/memory.go`) is
  now the default — a mutex-guarded per-key counter, same atomicity
  guarantee as the Redis Lua script but with no network hop, since a
  single process only needs to serialize against itself. The Redis
  version moved to `RedisStore` (`internal/budget/redis.go`, same
  `NewRedisStore` constructor as before, just renamed) and is now opt-in,
  for the case `MemoryStore` genuinely can't cover: multiple Governor
  processes that need to agree on one shared running total, or a cap that
  survives a restart. `budget_test.go` now runs every correctness test
  (allow-under-cap, deny-over-cap, the 200-goroutine concurrency proof)
  against both backends via a table-driven `backends()` helper, so they're
  held to the same bar. `BenchmarkReserve` shows why this was worth doing
  beyond the pitch: `MemoryStore` is ~25ns/op vs. `RedisStore`'s ~95µs/op,
  roughly 3,700x — the network hop was real, avoidable cost for the
  common single-process case.
