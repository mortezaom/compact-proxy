# Reliability

## Invariants

- `/health` is local liveness; `/ready` requires at least one locally usable or
  refreshable auth account.
- Access tokens refresh before expiry when possible. An upstream `401` gets one
  refresh retry.
- Account failover is bounded to configured accounts and limited to connection,
  authentication, permission, quota, and rate-limit failures.
- Native Responses SSE is not fully buffered and preserves byte/event order.
- Dropping a downstream response drops its upstream stream; stream guards track
  normal completion versus cancellation.
- Model discovery caches for five minutes and serves the last successful
  catalog when a refresh temporarily fails.
- Account affinity is in memory, capped at 4096 sessions, and expires after
  four hours.

## Error handling

Upstream status codes and useful messages are mapped to OpenAI-style JSON.
Retries are bounded; ordinary server errors and ambiguous post-send failures
are not replayed across accounts. Logs contain request IDs and hashed account
aliases, never request bodies, tokens, or full auth headers.

## Verification

```bash
gofmt -l cmd internal
go test -count=1 ./...
go vet ./...
mkdir -p build
go build -trimpath -ldflags "-s -w" -o build/cproxy ./cmd/compact-proxy
```

The repository `Makefile` provides the same checks as a contributor
convenience.

Protocol changes should also be exercised against real Responses SSE, tool
calls, cancellation, OAuth refresh, model discovery, and a multi-turn Crush
session.
