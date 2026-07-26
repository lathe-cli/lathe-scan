# lathe-scan

Discover API specs across repositories and emit one draft
[Lathe](https://github.com/lathe-cli/lathe) `sources.yaml`, plus an audit report
and human-readable gaps.

`lathe-scan` is local, offline, and read-only with respect to scanned inputs. It
aggregates APIs from multiple repositories without executing their code or
generating a CLI. Lathe consumes the output only after human confirmation.

## Install

```sh
go install github.com/lathe-cli/lathe-scan@latest

# From a clone
make build
./bin/lathe-scan --version
```

Go 1.25 or newer is required when building from source.

## Quick start

```sh
# Build one manifest from multiple services
lathe-scan ../billing ../inventory --out ./out

# Add or refresh a service while preserving existing entries and policy
lathe-scan ../shipping --out ./out --merge

# Generate a CLI after reviewing sources.yaml and GAPS.md
cd ./out
lathe sync-specs
lathe gen
```

Inputs may be repository directories or `.zip` archives. Discovery supports:

- OpenAPI 3, Swagger 2, Proto, GraphQL, and Postman collections.
- Static route extraction for FastAPI, Flask, Django, Spring, NestJS, Express,
  Fastify, Gin, Echo, Chi, Rails, Laravel, ASP.NET, Ktor, Actix, and Axum.

An existing native spec takes precedence over static extraction. Unresolved or
unsafe assumptions are reported, not silently guessed.

## Command

```text
lathe-scan <input>... --out <dir> [--name <source>] [--prefer <backend>] [--merge] [--force] [--json]
```

- `--out` selects the output directory and is required.
- `--name` overrides the name when exactly one source is produced.
- `--prefer openapi3|swagger|proto|graphql` breaks otherwise equal backend
  choices; it never outranks a source that would generate more commands.
- `--merge` updates sources owned by the scanned inputs while preserving foreign
  sources and hand-written policy. A failed input keeps its previous entries.
- `--force` permits writing into a non-empty output directory.
- `--json` also prints `report.json` to stdout.

Run `lathe-scan --help` for the current flag reference.

Exit codes are `0` for at least one usable source, `1` for usage errors, `2` when
nothing usable is found, and `3` for write failures.

## Output

```text
<out>/
  sources.yaml   # draft Lathe source map
  report.json    # candidates, provenance, confidence, and gaps
  GAPS.md        # blocking and advisory review items
  <source>/...   # copied material for local_path sources
```

Only sources expected to generate at least one Lathe command are emitted.
`report.json` and `GAPS.md` are still written for an empty result. Identical
inputs produce deterministic manifests and reports.

`--merge` never widens a hand-trimmed GraphQL exposure policy. It also never
deletes copied directories from earlier runs; only `sources.yaml` may be
rewritten or removed when the current result requires it.

## Documentation

- [Design](docs/DESIGN.md) defines architecture, merge, confidence, and safety
  contracts.
- [Contributing](CONTRIBUTING.md) covers development commands and pull requests.

## License

[MIT](LICENSE) © lathe-cli
