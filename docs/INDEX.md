# Documentation Index

`README.md` is the complete user guide for the Go application: source
installation, the `cproxy` commands, configuration, local storage, API routes,
security, and troubleshooting. The examples there call the binary directly;
the repository `Makefile` is only a contributor convenience.

This directory contains deeper engineering notes and the planning protocol.

## Engineering Documents

- `ARCHITECTURE.md`: service shape, module responsibilities, and request flow.
- `CONFIGURATION.md`: config-file fields, environment mapping, and precedence.
- `RELIABILITY.md`: runtime invariants, verification expectations, and failure
  handling.
- `SECURITY.md`: authentication boundaries, secret handling, and safe logging.
- `PLANS.md`: when and how to capture execution plans for agent work.

## Maintenance Rules

- Update docs in the same change as behavior when a route, configuration
  option, auth flow, or operational assumption changes.
- Prefer short, current documents over long manuals. Delete stale guidance
  rather than preserving contradictory history.
- Promote repeated review feedback into tests, lints, scripts, or explicit
  docs so future agents can discover it from the repo.
