# Agent Operating Guide

This repository is a Go/Gin proxy that exposes OpenAI-compatible endpoints
backed by a ChatGPT/Codex subscription. The active application lives in
`cmd/` and `internal/`. Treat this file as the short table of contents for
agent work: keep direct instructions concise, and move durable design,
architecture, reliability, or product notes into the versioned docs linked
below.

## Harness Engineering Protocol

- Humans steer; agents execute. Convert requests into concrete acceptance
  criteria, implement the change, verify it locally, and report the result.
- Prefer depth-first execution. Break large work into small capabilities,
  land the missing capability, then use it to unlock the next step.
- When a task fails, diagnose the missing tool, invariant, documentation, or
  feedback loop. Do not paper over failures with retries alone.
- Keep repository knowledge agent-legible. If a decision matters after this
  task, encode it in code, tests, CI, or docs rather than relying on chat
  context.
- Enforce invariants mechanically when practical. Prefer tests, type checks,
  compiler errors, or lints over prose-only rules.
- Optimize for maintainable agent throughput: small scoped changes, clear
  commands, reproducible verification, and minimal hidden state.

## Project Map

- `docs/INDEX.md`: documentation map and system-of-record entrypoint.
- `docs/ARCHITECTURE.md`: service structure, request flow, and module
  ownership.
- `docs/CONFIGURATION.md`: config-file fields, environment mapping, and
  precedence.
- `docs/RELIABILITY.md`: operational invariants, verification, and failure
  handling.
- `docs/SECURITY.md`: auth, secret handling, and boundary expectations.
- `docs/PLANS.md`: planning protocol for larger or multi-step work.
- `cmd/compact-proxy/main.go`: process entrypoint.
- `internal/app/app.go`: CLI entrypoint, Gin router, middleware, and command
  dispatch.
- `internal/app/core/auth.go`: OAuth/device login, token persistence, refresh,
  and revocation.
- `internal/app/core/config.go`: TOML/environment resolution, shared
  application state, and Codex client version handling.
- `internal/app/core/models.go`: model listing and upstream authentication
  header helpers.
- `internal/app/handlers/proxy.go`: `/v1/responses` passthrough and SSE
  collection/streaming.
- `internal/app/handlers/chat.go`: OpenAI chat completions translation,
  streaming, tool calls, and reasoning suffix parsing.
- `internal/app/handlers/images.go`: OpenAI-compatible image generation/edit
  endpoints.
- `internal/app/handlers/usage.go`: usage endpoint handling.
If you add substantial new behavior, add or update docs that explain the new
route, data boundary, or operational assumption. Keep `AGENTS.md` short; use
linked docs for deeper design records.

## Go Standards

- Preserve the existing Go package layout and format with `gofmt`.
- Resolve deployment settings through `core.Config` and pass the immutable
  configuration through `AppState`; do not add new request-time environment
  lookups when a setting belongs in the config seam.
- Parse and validate external request shapes with typed JSON structures at the
  boundary. Keep dynamic maps only where upstream fields are genuinely open.
- Keep request cancellation attached to the Gin request context and avoid
  blocking work inside request handlers.
- Keep logs useful without exposing access tokens, refresh tokens, API keys,
  account secrets, or full auth headers.
- Preserve OpenAI-compatible response shapes and SSE behavior when touching
  `/v1/responses`, `/v1/chat/completions`, or image endpoints.

## Security And Reliability

- Treat `~/.compact-proxy/` contents, `PROXY_API_KEY`, bearer tokens, refresh
  tokens, and upstream authorization headers as secrets.
- `/health` may remain unauthenticated. Other endpoints must continue to honor
  `PROXY_API_KEY` when it is configured.
- Keep token refresh behavior deterministic: refresh before expiry when
  possible, retry once on upstream `401`, and surface clear errors after that.
- Be careful with CORS and auth middleware ordering. Validate behavior after
  changing either one.
- Avoid introducing persistent server-side conversation state unless the
  stateless contract in `README.md` is intentionally revised.

## Verification

Run the smallest useful verification for the change, then broaden when the
blast radius warrants it.

Recommended commands:

```bash
gofmt -l cmd internal
go test -count=1 ./...
go vet ./...
mkdir -p build
go build -trimpath -ldflags "-s -w" -o build/cproxy ./cmd/compact-proxy
```

The repository `Makefile` wraps these checks as an optional contributor
convenience.

For endpoint or streaming changes, also run the server locally and exercise the
affected route with `curl` or an SDK client. Do not require live upstream calls
for purely local parser or translator tests when unit coverage can prove the
behavior.

## Change Discipline

- Before editing, inspect the relevant files and current git status.
- Do not overwrite user changes. If files are already modified, read them and
  work with the existing state.
- Keep changes tightly scoped to the task. Avoid drive-by refactors,
  dependency churn, or unrelated formatting.
- Prefer adding focused tests near the changed behavior. If test coverage is
  missing and the change affects protocol translation, auth, or streaming,
  add coverage unless there is a concrete blocker.
- After edits, summarize changed files, verification performed, and any
  residual risk or commands that could not be run.

## Documentation Practice

- `README.md` is the user-facing contract for setup, commands, endpoints, and
  limitations. Update it when behavior visible to users changes.
- Use `docs/` as the system of record for durable decisions:
  `docs/ARCHITECTURE.md` for structure, `docs/RELIABILITY.md` for operational
  behavior, `docs/SECURITY.md` for auth/secrets boundaries,
  `docs/CONFIGURATION.md` for settings, and `docs/PLANS.md` for multi-step
  work.
- Prefer a map plus links over a monolithic manual. Keep guidance fresh by
  deleting stale instructions when behavior changes.
