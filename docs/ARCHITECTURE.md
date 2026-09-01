# Architecture

Compact Proxy is a Go/Gin transport proxy between OpenAI-compatible clients and
the ChatGPT Codex backend. The Responses API is the native path; compatibility
endpoints translate into Responses without changing native Responses behavior.

## Request flow

1. `internal/app/app.go` applies request IDs, request logging, CORS, and the
   required proxy API key.
2. `internal/app/handlers/proxy.go` minimally normalizes Responses fields,
   reasoning, conversation mode, and prompt-cache affinity.
3. `internal/app/core/upstream.go` selects an authenticated account, refreshes
   once on `401`,
   performs bounded safe failover, and preserves upstream errors.
4. Native SSE bytes stream directly downstream. The chat, image, and Anthropic
   handler modules translate only their compatibility protocols.

No conversation messages or response history are persisted or shared across
HTTP requests. A WebSocket connection retains only its latest request to serve
an explicit `response.append`. Explicit `previous_response_id` and
`item_reference` values can pass through, but the proxy never introduces hidden
chaining.

## Module ownership

- `cmd/compact-proxy/main.go`: process entrypoint.
- `internal/app/app.go`: CLI, routes, middleware, health, readiness, and Crush
  setup.
- `internal/app/core/auth.go`: PKCE/device OAuth, persistence, refresh,
  revocation, and accounts.
- `internal/app/core/config.go`: TOML/environment resolution, shared clients,
  and state.
- `internal/app/core/routing.go`: hashed session keys, prompt-cache keys, and
  account affinity.
- `internal/app/core/upstream.go`: shared authenticated upstream request and
  failover policy.
- `internal/app/core/models.go`: dynamic catalog cache and capability endpoints.
- `internal/app/handlers/proxy.go`: native Responses HTTP/SSE and compact
  handling.
- `internal/app/handlers/websocket.go`: Responses WebSocket-to-HTTP/SSE bridge.
- `internal/app/handlers/chat.go`, `images.go`, `anthropic.go`: isolated
  compatibility translations.
- `internal/app/handlers/usage.go`: Codex rate-limit snapshots.
- `internal/app/core/metrics.go`: content-free counters and stream lifecycle tracking.

## Data boundaries

Known request shapes use typed JSON structures. The native Responses body
remains dynamic so unknown upstream-supported fields survive normalization.
Session affinity stores only hashes and account routing keys in a bounded,
four-hour in-memory cache.

## Configuration seam

`core.Config` is resolved once at startup. `AppState` owns that resolved value,
so HTTP handlers and authentication flows do not read deployment environment
variables independently. File values are overridden by environment variables,
and serve flags override both. This keeps configuration behavior testable and
prevents a request from observing a different configuration halfway through a
process lifetime. The same seam owns the user-local persistence root at
`~/.compact-proxy`, including the default TOML file, auth files, backups, and
generated proxy key; explicit paths remain supported as intentional overrides.
