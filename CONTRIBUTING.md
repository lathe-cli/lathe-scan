# Contributing to lathe-scan

Read [docs/DESIGN.md](docs/DESIGN.md) before changing behavior; it defines the
non-negotiable product and safety boundaries.

## Development

Go 1.25 or newer and `golangci-lint` are required.

```sh
make build   # build ./bin/lathe-scan
make test    # run go test ./...
make fmt     # format Go sources
make check   # format check, vet, lint, and tests
```

Run `make check` before opening a pull request. Use `make tidy` only when module
dependencies change.

## Code and Tests

Follow standard Go naming and let `gofmt` handle formatting. Wrap errors with
context using `fmt.Errorf("...: %w", err)`. Keep discovery and output
deterministic.

Place tests beside the package they exercise as `*_test.go`; use package-local
`testdata/` for representative files. Add focused coverage for changed parsing,
selection, merge, security, or output behavior. The project has no numeric
coverage threshold.

## Commits and Pull Requests

Use scoped Conventional Commits, for example
`fix(scan): preserve source provenance`. Sign every commit with
`git commit -s`.

Keep pull requests focused. Explain the behavior and motivation, link relevant
issues, and list exact verification commands. Include sample terminal output
when reports, manifests, flags, or exit behavior change.
