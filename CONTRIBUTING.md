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

`make bench` measures discovery recall against the pinned real-repository
corpus in `bench/corpus.yaml`. Run it when changing discovery, extraction, or
selection, and update a pin only deliberately. A `known_gap` entry that starts
passing should be promoted to a scored entry in the same change.

`make contract` scans the fixture under `bench/contract/` and runs `lathe`
built from the latest upstream main commit against the result: `specsync`
must load and stage the manifest and `codegen` must emit commands. The
scanner mirrors Lathe's rules without importing them, so this gate is what
catches mirror drift; run it locally before changing anything that shapes
`sources.yaml`.

Both targets need network access and never run in `make check` or on pull
requests. The Drift workflow runs them after each merge to main, weekly, and
on manual dispatch; a red Drift run means main drifted relative to the world,
not that a specific diff is wrong.

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
