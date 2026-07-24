# lathe-scan

Discover API specs across one or more repositories — offline and read-only — and
emit a single draft [Lathe](https://github.com/lathe-cli/lathe) `sources.yaml`,
plus a machine-readable `report.json` and a human `GAPS.md` for confirmation.

`lathe-scan` is the cold-start front end for Lathe: it turns the scattered,
messy reality of "one product, one CLI, but many modules across many repos" into
one aggregated, reproducible spec manifest that Lathe can generate a CLI from.

- **Offline & read-only.** No network, no upload; scanned inputs are never modified.
- **Aggregates many inputs into one manifest.** Each discovered API becomes its own
  named source — exactly the map shape Lathe already consumes.
- **Reproducible origins.** For a git input with an immutable ref, it emits
  `repo_url` + `pinned_tag`; otherwise `local_path`. It never fabricates an origin.
- **Honest confidence.** A source is "usable" only if Lathe would actually generate
  at least one command from it; everything unresolved is reported as a gap.

`lathe-scan` is a sibling to Lathe. It does not import Lathe's internal packages and
never runs code generation itself — Lathe consumes its human-confirmed output.

## Install

```sh
go install github.com/lathe-cli/lathe-scan@latest
# or, from a clone:
make build   # -> ./bin/lathe-scan
```

## Usage

```text
lathe-scan <input>... --out <dir> [--name <source>] [--prefer openapi3|swagger|proto|graphql] [--merge] [--force] [--json]
```

- `<input>...` — one or more repo directories (zip input is planned).
- `--out` — output directory (required).
- `--name` — source name; only valid when exactly one source is produced.
- `--merge` — fold results into an existing `--out/sources.yaml`, preserving foreign sources.
- `--force` — allow writing into a non-empty `--out`.
- `--json` — also print `report.json` to stdout.

Exit codes: `0` at least one usable source · `1` usage error · `2` nothing usable
found across all inputs · `3` write failure.

### Example

```sh
# Aggregate two service repos into one Lathe manifest
lathe-scan ../billing ../inventory --out ./out

# Then hand the confirmed output to Lathe
cd ./out && lathe sync-specs && lathe gen
```

## Output

```text
<out>/
  sources.yaml     # draft Lathe sourceconfig — one or more named sources
  report.json      # per-input/per-candidate scores, selection, confidence, gaps
  GAPS.md          # human-readable gaps, blocking ones first
  <source>/...     # copied spec material, for local_path sources only
```

Output is deterministic: identical inputs produce byte-identical `sources.yaml`
and `report.json` (no timestamps; the only version marker is `tool_version`).

## Capability levels

| Level | Scope | Status |
|-------|-------|--------|
| **L1** | Find & select existing API specs (OpenAPI 3, Swagger 2, Proto, GraphQL, Postman) | OpenAPI 3 + Swagger 2 implemented; Proto/GraphQL/Postman planned |
| **L2** | Allowlisted static framework extractors (FastAPI, Spring, NestJS, Gin, Echo, Chi, Express/Fastify) → honest OpenAPI 3 draft | Planned |
| **L3** | Experimental extractors, Postman→OpenAPI normalization | Not started |

L2 never starts the application, imports modules, runs builds, or executes any
scanned code — it is static analysis only. See [docs/DESIGN.md](docs/DESIGN.md)
for the full design.

## Development

```sh
make help    # list targets
make build   # build ./bin/lathe-scan
make check   # fmt-check, vet, lint, test
```

Requires Go 1.25+.

## License

[MIT](LICENSE) © lathe-cli
