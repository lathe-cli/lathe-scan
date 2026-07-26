# Repository Guidelines

## Sources of Truth

Use [README.md](README.md) for the public interface,
[docs/DESIGN.md](docs/DESIGN.md) for architecture and safety contracts, and
[CONTRIBUTING.md](CONTRIBUTING.md) for the contributor workflow. Read current
code and tests before changing behavior; if they conflict with the design,
surface the mismatch instead of silently choosing one.

## Repository Map

`main.go` starts the CLI. `internal/cli/` owns flags and exit codes.
`internal/scan/` owns discovery, parsing, static extraction, provenance,
reporting, and writes. Tests are package-local `*_test.go` files. Build output
belongs in `bin/`; local scratch belongs in `.local/`.

## Change Rules

Keep changes inside the scanner boundary defined in DESIGN. Preserve
deterministic output, honest confidence, complete reference closures, and
fail-closed handling of user-edited GraphQL exposure.

Put user instructions in README, durable behavior and rationale in DESIGN, and
development process in CONTRIBUTING. Do not duplicate the same rule across
documents.

## Verification

For code changes, run the narrowest relevant test first, then `make check`.
Protect stable parsing, merge, security, and output invariants with focused
package-local tests. For documentation-only changes, verify links, commands,
and `git diff --check`; do not claim runtime behavior without checking the
implementation.
