# lathe-scan Design

## Purpose and Boundaries

`lathe-scan` is Lathe's cold-start discovery tool. It turns API artifacts spread
across repositories into one draft `sources.yaml`, where each API is an
independent named source. Lathe consumes that manifest only after human review.

The scanner is an independent local binary and does not import Lathe internals.
It is offline and read-only with respect to scanned inputs. It never uploads
source, starts services, imports application modules, runs builds, installs
dependencies, or generates a CLI.

L1 discovery and L2 static extraction are product scope. L3 denotes
experimental inference with a higher false-positive risk.

## Processing Model

Each run follows one deterministic pipeline:

1. Normalize and de-duplicate input paths. Extract ZIP inputs into private
   temporary storage after validating their contents.
2. Resolve each input's origin locally and index its files once.
3. Run L1 parsers over existing API artifacts.
4. Run L2 only when that input has no usable L1 source.
5. Reject sources that Lathe would load but generate no commands from.
6. Group usable candidates by logical source, recommend one per group, and emit
   only those recommendations.
7. Assign stable names, write the manifest, and report every usable candidate
   in `report.json` and `GAPS.md`.

One input may yield multiple source candidates. Multiple directories or
archives are aggregated into one source map; no monorepo is collapsed to a
single API. Candidates compete as alternatives only when they share a derived
base name and location lineage; recognized version directories such as `v1` and
`master` belong to the same lineage. Generic titles alone never collapse APIs
from unrelated service directories. The report retains every usable candidate
and `sources.yaml` receives one recommendation per group.

## L1: Existing API Artifacts

L1 recognizes OpenAPI 3, Swagger 2, Proto, GraphQL, and Postman collections.
Discovery respects `.gitignore` and excludes dependency, build, vendored, test,
fixture, sample, example, and generated trees. A spec the repository treats as
generated or third-party is not selected as its contract.

Every candidate is parsed and recorded, including failures. Candidates are
de-duplicated by content hash and scored by usable API surface rather than file
name alone:

- OpenAPI and Swagger use paths, operations, schemas, version, and metadata.
- Proto requires services and RPCs; `google.api.http` annotations decide whether
  Lathe can generate commands.
- GraphQL requires valid SDL, roots, and exposed operations.
- Postman requires concrete requests and competes as an `openapi3` backend after
  conversion; it loses only on lower command yield, `--prefer`, or name order.

`--prefer` breaks backend ties only after command yield. It cannot make a
smaller or unusable source win.

OpenAPI and Swagger `$ref` files, and Proto imports, are resolved as a complete
closure. Local sources copy that closure; pinned sources list repository-relative
paths. An unresolved closure is a blocking gap.

Backend-specific rules:

- OpenAPI 3 uses non-empty relative `openapi3.files`; Swagger 2 uses
  `swagger.files` and is never silently converted.
- Proto uses `entries` and `staging`. Trees without HTTP annotations are
  reported but not emitted. Imports are traversed from service entries and
  resolved in order: repository files, then provider trees that discovery skips
  as third-party, then dependencies pinned by v1/v2 `buf.lock` or `go.mod` plus
  `go.sum`. Only `google/protobuf/*` is compiler-provided. A pin is emitted only
  in the form Lathe accepts, so a shape it would reject stays an unresolved
  import. Imports carrying the repository's Go module prefix are mapped back to
  that module root only when the whole closure lives under it. Compilation
  remains an explicit verification gap.
- GraphQL uses one schema path and an explicit non-empty `expose` policy. The
  discovered surface is proposed for review, never treated as approved.
- Postman is converted to a medium-confidence OpenAPI 3 draft with a
  `postman-needs-conversion` gap.

`default_hostname` is extracted only when an OpenAPI server or Swagger host is
unambiguous.

## L2: Static Framework Extraction

L2 supports FastAPI, Flask, Django, Spring, NestJS, Express, Fastify, Gin, Echo,
Chi, Rails, Laravel, ASP.NET, Ktor, Actix, and Axum. It examines files
independently, selects the extractor with the most evidenced routes, and uses a
fixed priority for ties.

An extractor may recover only statically visible methods, paths, and path
parameters. It does not guess authentication, bodies, responses, dynamic
routes, or cross-file data flow. Django routes default to GET with an explicit
method-verification gap because its URL configuration does not declare the
method.

The generated OpenAPI draft carries operation confidence, gaps, and source-file
provenance. Unresolved routes are omitted and reported. Matching never spans
file boundaries, and hitting the file cap produces a `scan-truncated` gap rather
than a claim of complete coverage.

## Validity and Confidence

"Usable" means Lathe is expected to generate at least one command, not merely
that a file parses. A Proto tree without HTTP annotations or a GraphQL source
whose exposure matches nothing is not usable.

- `high`: OpenAPI 3, Swagger, or GraphQL yields commands with no blocking gaps.
- `medium`: Proto (staging and imports are inferred), Postman conversion, or L2
  static extraction.
- `low`: the built candidate has blocking gaps (or would emit no commands).

Confidence applies to candidates and extracted operations. It is never a
blanket statement that the repository was fully understood.

## Origins, Names, and Determinism

Origin resolution is local:

1. A Git input with a remote and immutable tag at `HEAD`, or otherwise its
   40-character commit SHA, uses `repo_url` and `pinned_tag`.
2. Any input without a reproducible remote origin uses `local_path` and copied
   source material.

Floating refs are never emitted, and origins are never fabricated.

Source names derive from the repository, spec title, or input directory. They
are normalized into valid Go identifiers and receive deterministic collision
suffixes. Sorting, selection, naming, YAML, and JSON output must be stable;
identical inputs and version metadata produce byte-identical results.

## Merge and Ownership

`--merge` distinguishes scanner-owned entries from foreign or hand-written
entries through report provenance: absolute input path, backend, and a stable
location key. Identity is independent of source name and discovery order.

The merge contract is:

- Ownership is recovered from recommended `sources[]` and `preserved[]` in the
  prior report, so it survives repeated partial scans. Report-only candidates
  never claim a manifest entry.
- A source produced again updates the entry with matching provenance.
- Scanner-derived origin and backend fields are refreshed. Unknown fields and
  human policy such as `display_name`, `groups`, `output`, and `selection` are
  preserved.
- Policy follows exact provenance, never a reusable source name. Safety-critical
  GraphQL exposure policy remains in the report while its candidate is not
  recommended, so a later recommendation cannot widen the approved surface.
- Foreign entries are carried through untouched.
- If an input produces no usable result, its prior entries remain with a
  `source-kept` gap; the underlying failure remains blocking. A failed scan is
  not evidence that an API is gone.
- If a successfully scanned input produces a changed recommendation, owned
  entries that are no longer recommended leave the manifest.
- A non-empty manifest paired with a pre-provenance report is refused; ownership
  cannot be reconstructed safely.

GraphQL exposure fails closed. A preserved subset remains a subset. Empty,
unreadable, or wholly stale exposure leaves the prior entry unchanged, raises a
blocking gap, and never widens the approved surface.

No run deletes copied directories or other user files under `--out`. It may
rewrite `sources.yaml`, or remove that manifest when a non-merge run produces no
source. Reports are audit input, never authority to delete data.

## Report Contract

`report.json` is the machine-readable explanation for the run:

- `summary` records input, source, usable, Postman, and exit counts.
- `inputs[]` records origin, candidates, parse errors, scores, and selections.
- `sources[]` records every usable candidate found by this run with confidence,
  recommendation, command yield, provenance, metrics, and gaps. For an
  unmaterialized local candidate, `files` identifies its original scanned
  evidence; ZIP evidence has no local origin until selected and extracted.
- `policies[]` retains safety-critical human policy by exact provenance while a
  candidate is unmaterialized or its input is not scanned, without granting that
  candidate manifest ownership.
- `preserved[]` records manifest entries carried through a merge.
- `gaps[]` records unresolved global or input-level behavior.

Recommended, unblocked entries in `sources[]`, plus `preserved[]`, account for
the manifest. `summary.sources` counts reported candidates,
`summary.usable` counts entries emitted by the run, and `summary.exit_code`
matches the process result. An empty result still writes `report.json` and
`GAPS.md` with a blocking explanation.

Gap kinds form a stable vocabulary:

`auth`, `body`, `response`, `dynamic-route`, `proto-no-http-annotation`,
`graphql-expose-unconfirmed`, `postman-needs-conversion`,
`ambiguous-hostname`, `no-immutable-ref`, `parse-error`,
`ref-closure-unresolved`, `ref-closure-bundled`, `graphql-split-schema`,
`proto-imports-unverified`, `http-method-unverified`, `scan-truncated`,
`input-error`, `graphql-expose-preserved`, `graphql-expose-stale`,
`graphql-expose-empty`, `graphql-expose-unreadable`, and `source-kept`.

`GAPS.md` renders the same information for humans, with blocking items first.
The user-facing directory layout and exit codes belong in
[README.md](../README.md).

## Security Invariants

- Scanned inputs are never modified.
- Network access and execution of scanned code are prohibited.
- ZIP extraction rejects path traversal, symlinks, excessive entries, oversized
  files, and excessive total expansion.
- Discovery remains bounded and excludes ignored or third-party trees.
- A non-empty output directory requires explicit `--force` or `--merge`.
- Exposure policy and ambiguous behavior fail closed.

## Non-goals

- Source upload or a SaaS pipeline
- Arbitrary-language full API recovery
- Running target services or application code
- CLI generation, verification, or a marketplace
- Coupling scan success to a particular Lathe release
