# Security

## Client-facing boundary

The server defaults to `127.0.0.1`. Every route except `/health`, `/healthz`,
`/ready`, and `/readyz` requires the proxy API key through a bearer token or
`x-api-key`. A 256-bit key is generated and persisted on first use unless
`PROXY_API_KEY` or `security.api_key` is set. By default it is stored at
`~/.compact-proxy/proxy-api-key`; `PROXY_API_KEY_FILE` or
`security.api_key_file` controls its path. Generated files are written with
mode `0600` where the platform supports file modes.

Binding a non-loopback address emits a warning. Public exposure still requires
network-layer TLS and access controls appropriate to the deployment.

## Secrets

Treat OAuth access/refresh/ID tokens, auth files, proxy API keys, authorization
headers, and raw account identifiers as secrets. Upstream authorization always
comes from local OAuth storage, never from the client's bearer value.

Logs use hashed account aliases and short hashes for cache tags; they do not
include auth-file paths, request bodies, prompts, tool arguments, image data,
session IDs, or authorization headers. Health,
readiness, capabilities, metrics, and error responses do not expose OAuth
tokens or account identities.

## Persistence

`~/.compact-proxy/` is the default local storage root. The generated config,
primary auth file (`auth.json`), auth backups, and proxy key are stored there
unless an explicit path is configured. `auth.file` or `CODEX_AUTH_FILE` is the
primary read/write auth file. `auth.files` or `CODEX_AUTH_FILES` adds ordered
fallback files. Refresh preserves an existing refresh token when the provider
omits a replacement. Container mounts must be writable so refreshed
credentials and the generated proxy key survive restarts. See
`docs/CONFIGURATION.md` for precedence and path rules.

Prompt-cache and account affinity use a SHA-256 hash of `x-session-id` or
`x-session-affinity`; conversation content is never persisted.
