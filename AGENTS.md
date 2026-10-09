# itemize-ynab: agent and contributor conventions

Go CLI that splits Walmart card charges in YNAB by item category. User guide:
`README.md`. Product spec: `docs/itemize-ynab-PRD.md` (assumes a fork; where it
disagrees with the code, `docs/DECISIONS.md` wins). Design notes:
`docs/ARCHITECTURE.md`. This is an original project; the design reference
(eshaffer321/itemize) is unlicensed, so never copy its source.

## Architecture

Layering: CLI -> application -> domain <- adapters -> infrastructure.

- `internal/domain`: pure business rules (`order`, `categorizer`, `allocator`,
  `splitter`, `matcher`, `memo`). No HTTP, DB, file or clock access (time is
  passed in).
- `internal/application/sync`: the use case: orchestrator, writer, transaction
  source, summary model. Depends on domain and on small interfaces it defines.
- `internal/adapters`: `walmart` (order provider), `ynab` (client, plus the
  `ynabtest` fake server), `llm` (`anthropic`, `openai`, `llmhttp`).
- `internal/infrastructure`: `config` (YAML, `${VAR}` expansion, validation),
  `storage` (SQLite, embedded goose migrations) and `lock` (the run lock: a
  non-blocking `flock` on `<database.path>.lock`, taken by the CLI for each
  `walmart` run so runs never overlap). Infrastructure imports neither adapters
  nor domain.
- `internal/cli`: flags, wiring of real dependencies, summary printing, exit codes.
- `internal/version`: build version string.
- `cmd/itemize-ynab`: the binary. `cmd/spike-walmart`: throwaway live spike for
  John to run; never run by tests or CI.

Do not create empty placeholder packages.

## Rules

- TDD is mandatory: write the failing test first, see it fail, then implement.
- Logging: `log/slog`, by level (Debug/Info/Warn/Error). No `if verbose` branches.
- Before declaring done: `go test ./... -race` and `golangci-lint run` both clean
  (`make check`). New packages need at least 80% coverage.
- Money is integer cents (Walmart) or YNAB milliunits (cents x 10). Float to
  cents conversion happens in exactly one place.
- Use imports, never inline fully-qualified names.
- No ticket numbers in code comments.
- Secrets (YNAB token, Walmart cookies, LLM keys) never appear in logs, errors
  or output, and are never committed.
- YNAB tests run only against `httptest`, never a real plan. No live Walmart
  calls in tests.
- Commit format: `type: description` (feat, fix, refactor, docs, test, chore,
  perf, ci). No AI, Claude or Happy attribution lines.
- Do not commit or push until the diff has been reviewed.

## Exit codes

0 ok; 1 config, auth or usage error, another run holding the run lock, or Ctrl-C; 2 run finished with failed
charges; 3 stopped early by a YNAB rate limit or a Walmart block (stale session,
bot challenge, rate limit). Decided in one place: `cli.ExitCode`.

## Toolchain

Pinned in `.tool-versions` (asdf): Go and golangci-lint v2. `make check` runs
`go vet`, `golangci-lint run` and `go test ./... -race`. `make build` writes
`bin/itemize-ynab`.
