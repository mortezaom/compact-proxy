# Configuration

The Go application resolves one immutable configuration at startup. All
commands use the same resolved values, including serve, login, logout,
readiness, token refresh, and Crush setup.

## File discovery

Use TOML. Without an explicit path, the application reads
`~/.compact-proxy/config.toml`. The directory and file are created with secure
local defaults on first use. This is the single default configuration and
credential root for the application.

An explicit path is selected with --config PATH or
CODEX_OPENAI_PROXY_CONFIG. The command-line path wins if both are supplied.
The --config option can appear before or after the subcommand.

An explicit config path changes which TOML file is read, but does not relocate
the default auth file or generated proxy key. Use explicit auth/key paths when
you intentionally want storage elsewhere.

The file is read once at startup. Restart the process after changing it.
Unknown TOML fields and invalid values fail startup rather than being ignored.

## Precedence

The effective value is selected in this order:

~~~text
built-in defaults
    then TOML file
    then environment variables
    then serve command-line flags
~~~

Command-line flags currently override server.host, server.port, and
server.codex_version:

~~~text
cproxy --config ./config.toml serve --host 127.0.0.1 --port 8080
~~~

## TOML fields

| TOML field | Default | Purpose |
| --- | --- | --- |
| server.host | 127.0.0.1 | Listener bind address |
| server.port | 8080 | Listener port, from 1 through 65535 |
| server.codex_version | empty | Pin the client version; empty fetches it |
| auth.file | auth.json | Primary auth file, relative to ~/.compact-proxy |
| auth.files | empty list | Ordered additional auth files |
| security.api_key | empty | Inline proxy key; prefer an environment secret |
| security.api_key_file | proxy-api-key | Generated/read proxy-key path, relative to ~/.compact-proxy |
| defaults.reasoning_effort | empty | Fallback reasoning effort |
| defaults.conversation_mode | client | Default local conversation mode |
| defaults.usage_model | gpt-5.5 | Model used by the usage endpoint |

The application data directory is `~/.compact-proxy`. Relative auth paths,
auth backup paths, and proxy-key paths are resolved from that directory. Paths
may also be absolute or start with `~/` for an explicit user-home override.
This rule applies equally to TOML values and environment variables.

When auth.file is set, it is tried first. auth.files are then tried in the
listed order, with duplicate paths removed. If neither is configured, the
application uses `~/.compact-proxy/auth.json`.

## Environment mapping

| Environment variable | TOML field |
| --- | --- |
| HOST | server.host |
| PORT | server.port |
| CODEX_CLIENT_VERSION | server.codex_version |
| CODEX_AUTH_FILE | auth.file |
| CODEX_AUTH_FILES | auth.files, comma-separated |
| PROXY_API_KEY | security.api_key |
| PROXY_API_KEY_FILE | security.api_key_file |
| DEFAULT_REASONING_EFFORT | defaults.reasoning_effort |
| CONVERSATION_MODE | defaults.conversation_mode |
| USAGE_MODEL | defaults.usage_model |

These variables support local and container deployments. A non-empty
environment value overrides the corresponding TOML value. CODEX_AUTH_FILES
replaces the file list, so it does not unexpectedly merge accounts from two
sources.

## Security

The proxy key is a secret. Prefer PROXY_API_KEY_FILE, PROXY_API_KEY supplied by
the process environment, or a secret manager. Do not commit config.toml or put
an inline security.api_key in a shared configuration file. config.toml is
ignored by the repository's root .gitignore.

If no key is supplied, the application generates a random key and persists it
at security.api_key_file, or at `~/.compact-proxy/proxy-api-key` by default.
The key is loaded once when the server starts and then used by its auth
middleware.

OAuth endpoints, the OAuth client identifier, scopes, and the ChatGPT Codex
upstream URL remain fixed protocol constants. They are not user-configurable
because changing them would select a different authentication or upstream
contract, not merely change deployment behavior.

## Example

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

The repository includes the same shape, without user-specific paths, in
config.example.toml.
