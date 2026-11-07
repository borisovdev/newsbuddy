# Contributing to news-buddy

Thanks for your interest. news-buddy is a small single-user service, so the bar for new features is "does it fit a personal RSS-to-Telegram relay". Bug fixes, test fixtures for real-world broken feeds and documentation improvements are always welcome.

## Before you start

- For anything beyond a small fix, open an issue first and describe the problem and the change you have in mind. This avoids work on something that will not be merged.
- Read [AGENTS.md](AGENTS.md). It describes the architecture, package roles and the invariants the code relies on. It is written for AI agents but is the best overview for humans too.

## Development setup

Requirements:

- Go (version from `go.mod`)
- Docker with Compose, only if you want to run the service locally
- No C toolchain: the project is pure Go (`CGO_ENABLED=0`)

```bash
git clone https://github.com/borisovdev/newsbuddy.git
cd newsbuddy
make test
```

The test suite runs fully offline. You do not need a Telegram bot token to develop or test.

To run the real service against Telegram:

```bash
cp deploy/.env.example deploy/.env   # fill in the three required variables
make up
make logs
make down
```

## Making changes

### Run what CI runs

Before opening a pull request:

```bash
make fmt
make check            # gofmt, go vet, race tests, boundary checks, CGO_ENABLED=0 build
go mod tidy -diff     # must print nothing
```

CI also runs `govulncheck` and a multi-arch Docker build.

Useful while iterating:

```bash
go test ./internal/poller -run TestName -count=1   # single test
go test ./internal/app/apptest/...                  # end-to-end acceptance suite
make cover                                          # coverage summary
```

### Rules enforced by `make check`

- **Storage boundary.** `database/sql` and `Masterminds/squirrel` may only be imported under `internal/storage/sqlite/`. Everything else uses the `storage.Storage` interface.
- **Cache boundary.** `sync.Map` and `map[string]` may not appear in non-test `.go` files outside `internal/cache/`. This includes test helper packages (`storagetest`, `apptest`, `feedtest`, `tgtest`); use sorted slices there.
- **No cgo.** Do not add dependencies that require cgo.

### Invariants to preserve

These are deliberate design decisions. Changes that break them will not be accepted without prior discussion:

- **At-most-once delivery.** The poller order is `InsertPending` → render → send → `MarkPosted`. Do not reorder it. Interrupted posts are marked abandoned, never re-sent.
- **No backfill.** Adding a source marks every current item as seen in the same transaction.
- **Single wiring point.** Subsystems are wired only in `internal/app` (`app.New`). Only one `*tgbot.Bot` may exist.
- **`render` stays pure.** No I/O, logging, clock or config access.
- **Token redaction.** `config.Config` implements `slog.LogValuer` so the bot token never reaches the logs.

### Code conventions

- Format with `gofmt`. Keep the style of the surrounding code.
- Log event names are constants in `internal/logging/events.go` and are used as the slog message. Add new events there. Do not add the `component` attribute per call; `app.New` attaches it once per subsystem.
- Configuration is environment variables only. If you add one, document it in the README configuration table.
- Database migrations go in `internal/storage/sqlite/migrations/NNNN_name.sql`. They are forward-only: never edit a migration that has been released, add a new one instead.

### Tests

- Every bug fix should come with a test that fails without the fix.
- Changes to `storage.Storage` need contract cases in `internal/storage/storagetest`, which runs against both `memstore` and SQLite. Do not add per-implementation tests for interface behaviour.
- Use the existing fakes: `feed/feedtest.FakeFetcher`, `telegram/tgtest.FakePublisher`, `storage/memstore`.
- For behaviour that spans subsystems, add a scenario to `internal/app/apptest`.
- A malformed or unusual real-world feed belongs in `testdata/` as a fixture.
- Tests must not touch the network.

## Pull requests

- Keep each pull request focused on one change.
- Write commit messages in the imperative mood ("Add retry for 5xx feeds"), with a body explaining *why* when it is not obvious.
- Describe what changed and how you tested it. For user-visible changes (bot replies, message format, configuration), update the README.
- CI must be green before review.

## Reporting bugs

Open an issue with:

- the news-buddy version (image tag or commit),
- what you expected and what happened,
- relevant log lines (`LOG_LEVEL=debug` helps), **with the bot token and any private channel ids removed**,
- the feed URL, or a saved copy of the feed if it is private.

## Security

Do not report security issues in public issues. Use GitHub's private vulnerability reporting for this repository instead.

## License

news-buddy is licensed under the [GNU AGPL v3.0](LICENSE). By submitting a contribution, you agree that it is licensed under the same terms.
