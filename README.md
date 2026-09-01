# Compact Proxy (`cproxy`)

Compact Proxy is a Go service that exposes a ChatGPT/Codex OAuth subscription
through OpenAI-compatible APIs. Its source is organized under `cmd/` and
`internal/`.

There are no packaged releases or package-manager installers yet. The
supported installation path is to build the Go application from source.

## What it provides

- Native OpenAI Responses HTTP/SSE and WebSocket support
- OpenAI Chat Completions compatibility
- OpenAI-compatible image generation and image editing
- Anthropic Messages compatibility
- Browser PKCE login and device-code login for headless machines
- Automatic OAuth token refresh and ordered multi-account fallback
- Dynamic model discovery and per-model capability information
- Reasoning, tool-call, usage, cancellation, and streaming support
- A generated local proxy API key, request IDs, readiness, and Prometheus metrics
- TOML configuration with environment-variable and command-line overrides

## Requirements

- Go 1.27 or newer
- A POSIX shell for the commands in this guide (Linux and macOS)
- A ChatGPT/Codex account
- A browser for `login`, or a browser accessible separately from the proxy
  machine for `login-device`

## Installation

### Build from source

Clone the repository and build the binary into the ignored `build/` directory:

~~~bash
git clone https://github.com/mortezaom/codex-openai-proxy.git
cd codex-openai-proxy
go mod download
mkdir -p build
go build -trimpath -ldflags "-s -w" -o build/cproxy ./cmd/compact-proxy
~~~

The resulting executable is `build/cproxy`. The `-trimpath`, `-s`, and
`-w` flags remove local build paths, symbol data, and DWARF debug data from
this release-oriented build.

Run it from the checkout with:

~~~bash
./build/cproxy login
./build/cproxy serve
~~~

### Install `cproxy` into Go's bin directory

To make the exact `cproxy` command available from your `PATH`, build
directly to Go's configured binary directory:

~~~bash
go_bin="$(go env GOBIN)"
if [ -z "$go_bin" ]; then
  go_bin="$(go env GOPATH)/bin"
fi
mkdir -p "$go_bin"
go build -trimpath -ldflags "-s -w" -o "$go_bin/cproxy" ./cmd/compact-proxy
export PATH="$go_bin:$PATH"
~~~

The `export` applies to the current shell. Add the resolved directory to
your shell's normal `PATH` configuration if you want it available in future
terminals. Then use:

~~~bash
cproxy login
cproxy serve
~~~

### About `go install`

From a checkout, `go install ./cmd/compact-proxy` also installs the command in
Go's bin directory, but Go names it `compact-proxy` because that is the final
command-directory name. Use the `go build -o .../cproxy` command above when
the executable must be named `cproxy`.

There is not yet a documented remote `go install ...@version` command. Building
from a checkout keeps the repository and module paths explicit and reproducible.

### Makefile convenience

The repository also contains a Unix-oriented `Makefile` for contributors. It
is a convenience wrapper, not a requirement for installing or running the
application. The direct Go and binary commands above are the canonical
workflow.

## First run

1. Authenticate the proxy:

   ~~~bash
   ./build/cproxy login
   ~~~

   This opens a browser and listens for the OAuth callback on
   `127.0.0.1:1455`.

2. On a headless machine, use device login instead:

   ~~~bash
   ./build/cproxy login-device
   ~~~

   Open the displayed URL on any browser and enter the displayed code. The
   device code expires after 15 minutes.

3. Check authentication:

   ~~~bash
   ./build/cproxy auth status
   ~~~

4. Start the proxy:

   ~~~bash
   ./build/cproxy serve
   ~~~

The default listener is `127.0.0.1:8080`. Stop the foreground server with
`Ctrl-C`.

Check local liveness and authentication readiness from another terminal:

~~~bash
curl http://127.0.0.1:8080/health
curl http://127.0.0.1:8080/ready
~~~

`/ready` returns a successful response only when at least one configured auth
account is usable or refreshable.

## Binary commands

The examples below use `cproxy`. If you have not installed it into `PATH`,
replace `cproxy` with `./build/cproxy`.

### Command syntax

~~~text
cproxy [--config PATH] COMMAND [COMMAND OPTIONS]
~~~

The global `--config PATH` option, also available as `-c`, may appear before
or after the command:

~~~bash
cproxy --config ./config.toml serve
cproxy serve --config ./config.toml --host 127.0.0.1 --port 9000
~~~

### Available commands

| Command | Description |
| --- | --- |
| `serve` | Start the HTTP/WebSocket proxy server. |
| `login` | Authenticate through a browser OAuth flow. |
| `login-device` | Authenticate with a device code, for headless use. |
| `auth status` | Show configured auth status and token expiry. |
| `logout` | Revoke the primary account token and remove the primary auth file. |
| `setup crush` | Print Crush provider and explicit model configuration. |

### `serve` options

~~~text
cproxy serve [--host HOST] [--port PORT] [--codex-version VERSION]
~~~

- `--host` sets the bind address. Default: `127.0.0.1`.
- `--port` or `-p` sets the listening port. Default: `8080`.
- `--codex-version` pins the Codex client version sent upstream. When empty,
  the application discovers the current version and refreshes it periodically.

Examples:

~~~bash
cproxy serve --port 9000
cproxy serve --host 127.0.0.1 --port 9000
cproxy serve --codex-version 0.125.0
~~~

Binding to `0.0.0.0` or another non-loopback address is an explicit exposure
of the service. Use TLS and suitable network access controls when exposing it
beyond the local machine.

### Authentication commands

~~~bash
cproxy login
cproxy login-device
cproxy auth status
cproxy logout
~~~

`login` and `login-device` write the primary auth file. `logout` operates
on that primary file; configured backup auth files are not deleted.

### Crush setup

~~~bash
cproxy setup crush
cproxy setup crush --base-url http://127.0.0.1:8080/v1
~~~

The command prints an idempotent provider block and, when the proxy is
running, fetches `/v1/models` to print one `model add` command per model for
`crushrc`, including its scalar metadata. It also prints a complete
`crush.json` provider block so the `reasoning_levels` array can be preserved.
It does not edit Crush's configuration file automatically. If the proxy is
not running yet, it prints the provider block and asks you to run setup again
after starting it.

The `/v1/models` response also includes optional capability metadata such as
`can_reason`, `reasoning_levels`, and `default_reasoning_effort`. Crush builds
that metadata into its reasoning picker only when its discovery implementation
preserves provider model fields; the generated explicit model definitions work
around clients that discard discovered fields. The context window is copied
from the upstream catalog; `setup crush` does not replace it with an
unverified larger value.

## Configuration

Configuration is TOML. On first use, the application creates and reads:

~~~text
~/.compact-proxy/config.toml
~~~

The `~` means the current user's home directory, not the project directory.
For example, it is normally `/home/<user>` on Linux and `/Users/<user>` on
macOS.

Use the checked-in [config.example.toml](config.example.toml) as a starting
point for a separate configuration file.

### Configuration discovery

The application selects the config file in this order:

1. `--config PATH` or `-c PATH`
2. `CODEX_OPENAI_PROXY_CONFIG`
3. `~/.compact-proxy/config.toml`

The command-line path wins if both the option and environment variable are
present. An explicit config path must already exist and contain valid TOML.
The default file and its parent directory are created automatically.

The file is read once at startup. Restart `serve` after changing it. Unknown
TOML fields and invalid values fail startup instead of being silently ignored.

### Precedence

For settings that have all four sources, the highest-priority value wins:

~~~text
built-in defaults < TOML file < environment variables < serve flags
~~~

The `serve` flags override `server.host`, `server.port`, and
`server.codex_version`. Authentication and default-behavior values are set by
TOML or environment variables.

### TOML fields

| TOML field | Default | Description |
| --- | --- | --- |
| `server.host` | `127.0.0.1` | Listener bind address. |
| `server.port` | `8080` | Listener port from `1` through `65535`. |
| `server.codex_version` | empty | Pin the upstream Codex client version; empty enables discovery. |
| `auth.file` | `auth.json` | Primary auth file, relative to `~/.compact-proxy`. |
| `auth.files` | `[]` | Ordered fallback auth files. |
| `security.api_key` | empty | Inline proxy API key; prefer an environment variable or file. |
| `security.api_key_file` | `proxy-api-key` | Proxy key file, relative to `~/.compact-proxy`. |
| `defaults.reasoning_effort` | empty | Fallback reasoning effort. |
| `defaults.conversation_mode` | `client` | Local conversation mode: `client` or `server`. |
| `defaults.usage_model` | `gpt-5.5` | Model used when collecting `/usage`. |

Example:

~~~toml
[server]
host = "127.0.0.1"
port = 8080
codex_version = ""

[auth]
file = "auth.json"
files = ["auth.backup.json"]

[security]
api_key_file = "proxy-api-key"

[defaults]
reasoning_effort = "medium"
conversation_mode = "client"
usage_model = "gpt-5.5"
~~~

### Environment variables

| Environment variable | TOML field | Notes |
| --- | --- | --- |
| `HOST` | `server.host` | Bind address. |
| `PORT` | `server.port` | Integer from `1` through `65535`. |
| `CODEX_CLIENT_VERSION` | `server.codex_version` | Pin the upstream client version. |
| `CODEX_AUTH_FILE` | `auth.file` | Primary auth file. |
| `CODEX_AUTH_FILES` | `auth.files` | Comma-separated fallback files; replaces the TOML list. |
| `PROXY_API_KEY` | `security.api_key` | Proxy key value. |
| `PROXY_API_KEY_FILE` | `security.api_key_file` | Proxy key file. |
| `DEFAULT_REASONING_EFFORT` | `defaults.reasoning_effort` | One of the supported reasoning efforts. |
| `CONVERSATION_MODE` | `defaults.conversation_mode` | `client` or `server`. |
| `USAGE_MODEL` | `defaults.usage_model` | Model used for usage collection. |

Relative auth and proxy-key paths are resolved under `~/.compact-proxy`, even
when the TOML file itself is elsewhere. Absolute paths and paths beginning with
`~/` are explicit overrides. An explicit config file therefore does not move
credentials or generated secrets by itself; set explicit auth and key paths if
that is intentional.

## Local files and credentials

The default user-local layout is:

~~~text
~/.compact-proxy/
├── config.toml
├── auth.json
├── proxy-api-key
└── <optional auth backup files>
~~~

- `config.toml` is created automatically with safe defaults.
- `auth.json` stores the Codex-compatible OAuth token object written by the
  login commands.
- `auth.files` adds fallback files in the listed order. They are useful for
  multi-account routing.
- `proxy-api-key` is generated when the server first needs a proxy key and no
  key was supplied through configuration or the environment.

The application creates the directory with mode `0700` and generated files
with mode `0600` where the operating system supports file modes. Treat every
file in this directory as sensitive. Do not commit it or copy its contents
into client-side source code.

## Calling the API

The proxy listens at `http://127.0.0.1:8080` by default. The generated proxy
key is not the OAuth token: clients authenticate to the local proxy with the
proxy key, while the proxy uses the stored OAuth credentials upstream.

All routes except health/readiness checks require either:

- `Authorization: Bearer <proxy-key>`, or
- `x-api-key: <proxy-key>`.

For example:

~~~bash
proxy_key="$(cat ~/.compact-proxy/proxy-api-key)"
curl -H "Authorization: Bearer $proxy_key" \
  http://127.0.0.1:8080/v1/models
~~~

### Routes

| Method | Path | Purpose |
| --- | --- | --- |
| `GET` | `/health`, `/healthz` | Local liveness; no proxy key required. |
| `GET` | `/ready`, `/readyz` | Authentication readiness; no proxy key required. |
| `GET` | `/metrics` | Prometheus metrics. |
| `GET` | `/usage`, `/v1/usage` | Codex limits and credits. |
| `GET` | `/v1/models` | Cached dynamic OpenAI model list with optional capability metadata. |
| `GET` | `/v1/capabilities` | Discovered capabilities for all models. |
| `GET` | `/v1/models/{model}/capabilities` | Capabilities for one model. |
| `POST` | `/v1/responses` | Native Responses API with SSE streaming. |
| `GET` | `/v1/responses` | Responses WebSocket bridge. |
| `POST` | `/v1/responses/compact` | Upstream response compaction. |
| `POST` | `/v1/chat/completions` | Chat Completions compatibility. |
| `POST` | `/v1/images/generations` | Image generation compatibility. |
| `POST` | `/v1/images/edits` | Image editing compatibility. |
| `POST` | `/v1/messages` | Anthropic Messages compatibility. |

The model and capability routes discover the upstream catalog on demand and
cache it for five minutes. Use `/v1/models` to find a model ID before sending
a request.

### Responses request with `curl`

~~~bash
proxy_key="$(cat ~/.compact-proxy/proxy-api-key)"
curl http://127.0.0.1:8080/v1/responses \
  -H "Authorization: Bearer $proxy_key" \
  -H 'Content-Type: application/json' \
  -d '{"model":"<model-id>","input":"Hello","stream":true}'
~~~

`/v1/responses` forwards native SSE events when `stream` is true. A
non-streaming request is collected into a JSON response.

### OpenAI Python client

~~~python
from pathlib import Path

from openai import OpenAI

proxy_key = Path.home().joinpath(
    ".compact-proxy", "proxy-api-key"
).read_text().strip()

client = OpenAI(
    base_url="http://127.0.0.1:8080/v1",
    api_key=proxy_key,
)

response = client.responses.create(
    model="<model-id>",
    input="Hello",
)
print(response)
~~~

## Request behavior

- The native Responses path is forwarded with minimal normalization. Chat,
  image, and Anthropic requests are translated into the Responses protocol.
- If a request has `prompt_cache_key`, it is preserved. Otherwise,
  `x-session-id` or `x-session-affinity` is hashed into a deterministic
  cache key. Raw session values are not stored.
- The same session header keeps requests on the same usable account for up to
  four hours. Affinity is in memory and bounded to 4096 sessions.
- Conversation history remains client-owned. The proxy does not persist
  prompts, responses, or hidden conversation chains.
- Reasoning is selected in this order: native `reasoning`, compatible
  `reasoning_effort`, a supported model suffix such as `-low` or `-high`,
  then `defaults.reasoning_effort`.
- Access tokens refresh before expiry when possible. An upstream `401` gets one
  refresh retry, and configured accounts can be used as bounded fallbacks.

## Security and operations

The default loopback bind keeps the service local. If you choose a public bind:

- Keep proxy API-key authentication enabled.
- Put TLS and appropriate network access controls in front of the listener.
- Restrict permissions on `~/.compact-proxy/` and any explicit credential paths.
- Prefer `PROXY_API_KEY_FILE`, `PROXY_API_KEY`, or a secret manager over an
  inline `security.api_key` in a shared config file.

Logs use request IDs and hashed account aliases. They are designed not to log
OAuth tokens, proxy keys, full authorization headers, request bodies, prompts,
tool arguments, or raw account identifiers.

For deeper implementation notes, see:

- [Configuration reference](docs/CONFIGURATION.md)
- [Architecture](docs/ARCHITECTURE.md)
- [Security notes](docs/SECURITY.md)
- [Reliability notes](docs/RELIABILITY.md)

## Troubleshooting

### `cproxy` is not found

Run `./build/cproxy`, or put the Go binary directory used during installation
on your `PATH`.

### The server reports that authentication is not ready

Run `cproxy auth status` and then `cproxy login`. For a headless machine, use
`cproxy login-device`. A configured backup file is only useful if it contains a
valid, refreshable token.

### API requests return `401`

Read the local proxy key from `~/.compact-proxy/proxy-api-key` and send it as a
Bearer token or `x-api-key`. Do not use the OAuth token from `auth.json` as
the local proxy key.

### Browser login cannot complete

The browser flow needs local port `1455` for the OAuth callback. Close any
process using that port and retry. If the browser cannot be opened
automatically, the command prints the URL so it can be opened manually.

### Configuration changes are ignored

The config file is read only at startup. Stop and restart `serve`, and verify
that the intended file is selected with `--config` or
`CODEX_OPENAI_PROXY_CONFIG`.

## Development

The direct verification commands are:

~~~bash
gofmt -l cmd internal
go test -count=1 ./...
go test -race ./...
go vet ./...
mkdir -p build
go build -trimpath -ldflags "-s -w" -o build/cproxy ./cmd/compact-proxy
~~~

The repository `Makefile` wraps these checks for contributors, but it is not
required to build or run the proxy.

## License

MIT
