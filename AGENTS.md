# AGENTS.md

This file provides guidance to AI Agents when working with code in this repository.

## What this is

news-buddy: a single-user Go service that polls RSS/Atom feeds and posts new items to one Telegram channel, managed by the owner through bot commands (`/add`, `/list`, `/remove`, `/status`). All state is in SQLite. Configuration is environment variables only (see the README table). It is meant to run in Docker (`make up` / `make logs` / `make down`, using `deploy/.env` copied from `deploy/.env.example`).

## Commands

```bash
make test          # go test ./...
make test-race     # go test -race -count=1 ./...
make lint          # gofmt check + go vet
make fmt           # gofmt -w .
make check         # everything CI runs: lint, test-race, boundary checks, CGO_ENABLED=0 build
make cover         # coverage summary

go test ./internal/poller -run TestName -count=1   # single test
go test ./internal/app/apptest/...                  # offline end-to-end acceptance suite
```

CI (`.github/workflows/ci.yml`) also runs `go mod tidy -diff` and `govulncheck`, so keep `go.mod`/`go.sum` tidy. Tagging `v*.*.*` publishes a multi-arch image to GHCR.

## Enforced boundaries (`make check` fails on these)

- **Storage boundary**: `database/sql` and `Masterminds/squirrel` may only be imported under `internal/storage/sqlite/`. Everything else goes through the `storage.Storage` interface (`internal/storage/storage.go`).
- **Cache boundary**: `sync.Map` and `map[string]` may not appear in any non-test `.go` file outside `internal/cache/`. This includes test *helper* packages that are not `_test.go` files (`storagetest`, `apptest`, `feedtest`, `tgtest`), which use sorted slices instead.
- **No cgo**: the SQLite driver is `modernc.org/sqlite` (pure Go). Do not add cgo dependencies.

## Architecture

`cmd/news-buddy/main.go` loads config, builds the logger and hands off to `internal/app`, the composition root. `app.New` is the only place that wires subsystems together, the only place that builds the single `*tgbot.Bot` (a second one would get a 409 on `getUpdates`), and the only place that attaches the `component` log attribute (once per subsystem; do not add it per call). Every startup step is fatal; there is no degraded boot mode.

Construction order in `app.New`: sqlite store (runs embedded migrations) → in-memory cache + typed views → HTTP feed fetcher → `service` (source management + `SeenIndex`) → poller → bot handlers → `tgbot.Bot` → Telegram publisher → preflight → abandon sweep. There is a cycle (poller needs the publisher, the publisher needs the bot, the bot needs handlers, handlers need the poller), and `lazyPublisher` in `app.go` breaks it.

Package roles:
- `feed`: fetch (conditional GET with validators), parse (gofeed), normalize, and compute the per-item dedup key (`itemkey.go`).
- `service`: `SourceService` handles add/remove/list. Adding a source seeds every item currently in the feed as seen in the same transaction (no backfill). `SeenIndex` is the poller's dedup collaborator: the cache only speeds up the read-side filter and the DB is always the authority.
- `poller`: one goroutine, cycles never overlap. Per item: `InsertPending` (the dedup gate, `ON CONFLICT DO NOTHING`) → render → `SendWithFallback` (HTML, then plain-text fallback) → `MarkPosted`. This gives at-most-once delivery. At startup, rows that were inserted but never marked are stamped as abandoned and logged as `post.abandoned`, never re-sent. Do not reorder these steps.
- `render`: pure function from item to Telegram HTML/plain text. No I/O, logging, clock or config.
- `telegram`: publisher plus a pacer that rate-limits sends. `bot`: owner-only command handlers that only accept private chats.
- `cache`: a Redis-shaped `Cache` interface (string keys, `[]byte` values, every call takes a ctx and can fail) with typed views (`views.go`) on top.
- `logging`: event names are constants in `events.go` and are used as the slog message (`post.sent`, `poll.cycle.end`, …). Add new events there. `config.Config` implements `slog.LogValuer` so the bot token is redacted. Keep it that way.
- `storage/sqlite/migrations/NNNN_name.sql`: embedded and applied in lexical order. Forward-only, with no down migrations.

## Testing structure

- `storage/storagetest` is a shared contract suite that runs against both `memstore` (the fake) and `sqlite`. Any change to `storage.Storage` must keep both passing, so add contract cases there instead of per-implementation tests.
- Fakes: `feed/feedtest.FakeFetcher`, `telegram/tgtest.FakePublisher`, `storage/memstore`.
- `app/apptest` is the acceptance harness. It wires the real app with real SQLite in `t.TempDir()` against the fakes and drives cycles by hand with `RunCycle`. It runs fully offline. `app.WithPreflight` / `app.WithBotOptions` are the test seams that point the bot at a fake server.
- Feed fixtures live in `testdata/` (malformed XML, duplicate GUIDs, missing GUIDs/links, future dates, and so on).

## Comments

Explain behaviour in plain words. Do not cite IDs from private planning documents (requirement numbers, spec sections, edge-case numbers): readers of this repository cannot look them up.
