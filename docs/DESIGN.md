# lathe-scan

## Product

- Local-only binary. Offline. Read-only scan of one or more repo paths or zip archives.
- One product usually has one CLI but many modules, and those modules often live in
  different repos. `lathe-scan` therefore aggregates **many inputs into one draft
  `sources.yaml`**, mapping each discovered API to its own named source — exactly the
  shape Lathe already consumes.
- Three capability levels: L1 and L2 are product scope; L3 is experimental only.
- Output: draft Lathe-ready `sources.yaml` (one or more named sources), copied/pinned
  source material, `report.json`, and `GAPS.md`.
- No cloud, no upload, no CLI codegen, no verify pipeline, no MCP.

## Org / repo

- Independent repo: `lathe-cli/lathe-scan`
- Module: `github.com/lathe-cli/lathe-scan`
- Binary: `lathe-scan`
- Sibling to `lathe` / `kitup` / `lathe-registry`; does not import `lathe` internal packages.
- Lathe consumes human-confirmed outputs only; scan is upstream, never merged into lathe core.

## Aggregation model (one product CLI, many modules, many repos)

- Lathe's `sources.yaml` is a **map of named sources**. A single generated CLI is the
  aggregate of all sources in that map, so one product CLI = one `sources.yaml` = N
  modules, each an independent source with its own backend and its own origin.
- A single input may itself yield multiple sources (monorepo with several services).
  Never collapse a monorepo to one primary; emit every usable API as its own source.
- Multiple inputs (several repos, or several zips) merge into **one** `sources.yaml`.
- `--merge` adds/updates only the sources this scan owns and preserves any other
  sources and hand-edited policy already present in an existing `--out/sources.yaml`.
  This is how a new module in a new repo is folded into an existing product CLI.
  Ownership is recorded per source as `provenance` (absolute input, backend, and the
  source's location inside that input — spec path, proto root, collection file) in
  `report.json`, so a re-scan of the same input **updates** its entries rather than
  appending suffixed copies; merging the same input repeatedly is a no-op. The location
  is used deliberately instead of a positional key: a name must keep pointing at the
  same API when an unrelated source that sorts ahead of it appears. For the same reason
  the location must not be derived from the set of files found — an inferred proto root,
  the primary GraphQL schema, and the winning L2 extractor all move as a repo grows, so
  those three backends (at most one source per input each) key on a constant instead.
- Ownership is read from **both** `sources[]` and `preserved[]` in the previous
  `report.json`, and `preserved[]` carries provenance for entries this tool wrote.
  Ownership therefore survives any number of merges: scanning A, then B, then A again
  updates A's entry rather than adding a suffixed copy.
- Hand-written policy follows **provenance**, never a source name. Names are
  reusable — delete a spec and add a different one with the same title and backend and
  the new source inherits the freed name — so carrying policy by name would apply one
  API's decisions to another. Only an exact provenance match inherits the old entry.
- `--merge` overwrites only the keys scan derives inside an existing entry
  (`default_hostname`, `repo_url`, `pinned_tag`, `local_path`, `backend`, and the
  matching backend block's `files`/`entries`/`staging`/`schema`/`expose`). Every other
  key is handed back untouched — `display_name`, `groups`, `output`, `selection`, and
  anything else a human added. Preservation is by default, not by enumeration: decoding
  an entry into scan's own struct and re-encoding it would silently drop every field
  that struct does not model.
- A `graphql.expose` trimmed to a subset of the discovered surface is kept, reported as
  a `graphql-expose-preserved` gap. If the remembered expose no longer intersects the
  schema at all, every recorded decision is stale, and writing the newly discovered
  surface would publish operations nobody approved — so the entry is left exactly as it
  is, a blocking `graphql-expose-stale` gap is raised, and the run counts as having
  written nothing. Fail closed; never widen an exposure automatically. An expose that is
  already empty gets the same treatment under `graphql-expose-empty`: Lathe requires at
  least one query or mutation, so scan will neither pick the surface for the user nor
  report an ungeneratable entry as usable. An expose that cannot be read at all — a
  malformed hand edit — fails closed the same way under `graphql-expose-unreadable`:
  policy scan cannot read is policy it cannot honor, and overwriting it would publish the
  full discovered surface. Only the `expose` subtree is decoded, so an unrelated malformed
  key elsewhere in the entry never decides the fate of an exposure policy.
- No scan deletes anything under `--out` except the `sources.yaml` it rewrites. `--out`
  is a user directory and `report.json` is an ordinary editable file, so deriving
  deletions from it would let a hand-edited or corrupted report decide what is removed.
  Copied directories from earlier runs whose sources are gone are left in place; an
  unreferenced directory is far cheaper than deleted data.
- An input that produces **nothing** on a `--merge` — it failed to scan, or every source
  it yielded was blocking — keeps the entries it wrote earlier, exactly as they are, under
  a `source-kept` gap. Scan cannot tell "this API is gone" from "this input did not
  answer", and only one of those readings is recoverable: a mistyped path or a spec that
  stopped parsing would otherwise delete a working entry along with the policy a human
  wrote onto it. The blocking gap already says what went wrong; removing the entry stays
  the user's call. An input that *did* produce sources is the other case — it answered,
  so an entry it no longer accounts for is one its API really dropped, and that entry goes.
- A run that produces no source removes a stale `sources.yaml` rather than leaving a
  manifest this scan just contradicted. On `--merge` this cannot empty the file out from
  under a failed input: the kept entries above are still in it.
- Source names are derived deterministically (git remote name, then spec `info.title`,
  then input dir name), lowercased and collision-resolved by a stable suffix. `--name`
  is only valid when a single input yields a single source.

## Origin: prefer real pinned repos, fall back to local, never fabricate

Reproducibility is Lathe's core promise, so emit the most reproducible origin the input
actually supports. All detection is local and offline (reads `.git`, no network):

1. If the input is a git worktree with a remote and a resolvable **immutable** ref —
   a tag pointing at HEAD, else the 40-char HEAD SHA — emit `repo_url` + `pinned_tag`
   and record the spec's in-repo relative path in `files` (no copy needed; Lathe fetches
   it at sync time). Floating refs (`main`, `HEAD`, `refs/heads/*`) are rejected by Lathe
   and must never be emitted.
2. Otherwise emit `local_path` and copy the resolved source material into `--out`.
3. Never invent a `repo_url` or `pinned_tag`. `local_path` and `repo_url`/`pinned_tag`
   are mutually exclusive per source (Lathe enforces this).

## CLI

```text
lathe-scan <input>... --out <dir> [--name <source>] [--prefer openapi3|swagger|proto|graphql] [--merge] [--force] [--json]
```

- `<input>...`: one or more repo directories or zip archives.
- `--name`: only valid for a single source result.
- `--prefer`: break backend ties when choosing the `recommended` source. It never
  outranks a source that would emit more commands — a preferred backend that generates
  less is still the worse choice. An unknown value is a usage error.
- `--merge`: fold results into an existing `--out/sources.yaml`, preserving foreign sources.
- `--force`: allow overwriting a non-empty `--out` (default: refuse, to protect edits).
- `--version`: print the version `report.json` records as `tool_version`.

Exit: `0` at least one usable source written · `1` usage · `2` nothing usable found or
extracted across all inputs · `3` write failure

Only sources Lathe would generate at least one command from are written, so exit `0`
means the manifest is worth running Lathe on. `report.json` and `GAPS.md` are written
on exit `2` as well: "nothing usable" is a result that has to be explained, not just
signalled.

## Output layout (`--out`)

```text
sources.yaml           # draft Lathe sourceconfig, one or more named sources
report.json            # per-input and per-candidate scores, selection, confidence, gaps
GAPS.md                # human-readable gaps and required confirmation
<source>/...           # copied source material for local_path sources only
```

- Each `local_path` source gets its own subdirectory so files from different modules
  never collide; `local_path: <source>` resolves relative to `sources.yaml`.
- Pinned (`repo_url`) sources copy nothing; `files` point at in-repo relative paths.
- Preserve a selected Proto or GraphQL tree as-is; never force it into an `openapi.yaml`.
- Postman collections are converted to a synthesized OpenAPI 3 draft — the same honest
  shape L2 emits, medium confidence with an explicit `postman-needs-conversion` gap.
  In messy orgs the collection is often the only artifact that exists, so reporting it
  and stopping would throw away the highest-yield cold-start input. What is never done
  is presenting it as a native, high-confidence source.
- Never modify a scanned input. Extract zip input into private temporary storage; reject
  path traversal, symlink escape, and enforce size/entry caps against zip bombs.
- Selection and naming are deterministic: the same inputs always produce the same
  `sources.yaml` (stable sort, stable tie-breaks).

## report.json contract

`report.json` is the machine-readable audit of the scan: what was found, why each source
was chosen, and what a human must still confirm. It is the surface `--json` prints to
stdout and mirrors what `GAPS.md` renders for humans. Like `sources.yaml`, it is
deterministic — no wall-clock fields; the only version marker is `tool_version`.

```json
{
  "schema_version": 1,
  "tool_version": "0.1.0",
  "summary": {
    "inputs": 3,
    "sources": 5,
    "usable": 4,
    "postman_candidates": 1,
    "exit_code": 0
  },
  "inputs": [
    {
      "input": "../billing",
      "kind": "git",
      "origin": {
        "type": "repo_url",
        "repo_url": "https://github.com/acme/billing.git",
        "pinned_tag": "v2.3.1",
        "ref_kind": "tag"
      },
      "candidates": [
        {
          "path": "docs/openapi.yaml",
          "format": "openapi3",
          "parsed": true,
          "error": null,
          "content_hash": "sha256:…",
          "duplicate_of": null,
          "score": 87,
          "metrics": { "paths": 12, "operations": 34, "schemas": 20 },
          "reason": "OpenAPI 3.0.3, 12 paths, 34 operations, 20 schemas"
        }
      ],
      "selected": ["billing"]
    }
  ],
  "sources": [
    {
      "name": "billing",
      "recommended": true,
      "level": "L1",
      "extractor": null,
      "backend": "openapi3",
      "confidence": "high",
      "would_emit_commands": 34,
      "origin": {
        "type": "repo_url",
        "repo_url": "https://github.com/acme/billing.git",
        "pinned_tag": "v2.3.1",
        "ref_kind": "tag"
      },
      "default_hostname": "api.acme.com",
      "files": ["docs/openapi.yaml", "docs/schemas/user.yaml"],
      "metrics": { "paths": 12, "operations": 34, "schemas": 20 },
      "gaps": []
    }
  ],
  "gaps": [
    {
      "kind": "graphql-expose-unconfirmed",
      "scope": "source",
      "ref": "search",
      "message": "expose lists all 9 discovered queries and 3 mutations; confirm before generating.",
      "blocking": true
    }
  ]
}
```

Field rules:

- `summary.exit_code` mirrors the process exit code (`0` usable, `2` nothing usable), and
  `summary.usable` equals `len(sources)` — every source this run writes is one Lathe
  generates from, so the two can never disagree. A source that was built but then left
  untouched to protect existing policy is not counted: the run handed back nothing new.
- `sources[] + preserved[]` accounts for every entry in the written `sources.yaml`:
  `sources[]` is what this run produced, `preserved[]` lists what `--merge` carried
  over — each with the provenance that lets a later run recognize it as this tool's
  own again.
- `summary.postman_candidates` counts Postman collections seen across all inputs.
- `inputs[].origin` records how origin was resolved per input; `type` is `repo_url` or
  `local_path`, `ref_kind` is `tag` or `sha` (absent for `local_path`).
- `candidates[]` lists every parse attempt, usable or not. `parsed:false` carries a
  non-null `error`. `duplicate_of` points at the canonical candidate's `path` when
  content-hash dedup collapsed it; deduped candidates are never independently selected.
- `sources[]` mirrors the entries **this run wrote** into `sources.yaml`, enriched with
  the fields Lathe's config cannot hold: `recommended`, `level` (`L1`/`L2`), `extractor`
  (framework name or null), `confidence`, `would_emit_commands`, `metrics`, `input`,
  `provenance`, and resolved `gaps`. A draft that cannot be generated from is not listed
  here. On `--merge`, entries carried over from a previous manifest appear in
  `preserved[]` instead — together the two cover the manifest exactly.
- `would_emit_commands` is the concrete basis for `confidence`: `0` forces the source out
  of `high` and raises a blocking gap (proto without `google.api.http`, graphql whose
  `expose` matches nothing).
- Top-level `gaps[]` carries everything that is not attached to an emitted source: input
  errors, unpinnable worktrees, proto trees with no `google.api.http`, and the blocking
  gaps of drafts that were rejected rather than emitted. `GAPS.md` renders both levels,
  blocking first.
- `files` is the full reference/import closure, not just the root spec.
- `gaps[].kind` is a closed vocabulary: `auth`, `body`, `response`, `dynamic-route`,
  `proto-no-http-annotation`, `graphql-expose-unconfirmed`, `postman-needs-conversion`,
  `ambiguous-hostname`, `no-immutable-ref`, `parse-error`, `ref-closure-unresolved`,
  `ref-closure-bundled`, `graphql-split-schema`, `proto-imports-unverified`,
  `http-method-unverified`, `scan-truncated`, `input-error`, `graphql-expose-preserved`,
  `graphql-expose-stale`, `graphql-expose-empty`, `graphql-expose-unreadable`,
  `source-kept`. `scope` is `global`, `input`, `source`, or `operation`; `ref` names the
  source, the input, or `METHOD /path`.
- An empty run always carries a gap explaining itself. A spec file that would not parse is
  a blocking `parse-error` at input scope, and an L2 run where the file cap is what hid the
  routes is a blocking `scan-truncated` — reporting either only inside `candidates[]` would
  leave `GAPS.md` with nothing to show for exit 2.
- `blocking:true` means Lathe would reject or emit nothing until resolved; `false` is
  advisory. `GAPS.md` groups blocking gaps first.

## L1 — Spec hunter

L1 finds existing API artifacts before attempting source extraction.

### Discovery

- Scope: respect `.gitignore` and skip dependency/build/vendored trees by default
  (`node_modules/`, `vendor/`, `.venv/`, `dist/`, `build/`, `.git/`, `target/`,
  `site-packages/`), plus test scaffolding and shipped-sample trees (`testdata/`,
  `test*/`, `fixtures/`, `sample(s)/`, `example(s)/`, `generated/`, `third_party/`).
  Third-party specs shipped by dependencies are the main source of false positives;
  excluding them is a correctness requirement, not an optimization. `.gitignore` support
  covers negation, directory-only and anchored patterns, and `*`/`?`/`**`/`[class]`
  globs, with nested files overriding their parents. Scanning a subdirectory inherits the
  `.gitignore` files between it and the repository root, so `lathe-scan ./services/billing`
  excludes what `git check-ignore` excludes from the repo above it. Inheritance stops at a
  repository boundary: an input that is itself a repository root (has its own `.git`)
  inherits nothing from directories above it, exactly as git behaves.
- One tree walk per input feeds every detector; nothing re-walks.
- OpenAPI / Swagger: `openapi*.{yaml,yml,json}`, `swagger*.{yaml,yml,json}`, and
  candidates under `docs/`, `api/`, `openapi/`, `spec/`, and `apidocs/`.
- Postman: collection JSON files.
- Proto: `*.proto`.
- GraphQL: `*.graphql`, `*.graphqls`, and common schema filenames.

### Selection

1. Parse every candidate with its format parser; invalid candidates remain in the report
   with errors. De-duplicate by content hash so `openapi.json`/`openapi.yaml` copies and
   vendored duplicates collapse to one candidate.
2. Score usable candidates by format-specific completeness:
   - OpenAPI / Swagger: version, non-empty paths, operations, schemas, and metadata;
     prefer OpenAPI 3 when otherwise equivalent (both are native Lathe backends).
   - Proto: services, RPCs, imports, and — decisively — `google.api.http` annotations.
   - GraphQL: valid SDL, schema roots, queries, mutations, and types.
   - Postman: valid collection plus concrete requests, but never rank it above an equally
     complete Lathe-native source.
3. Emit **every** usable source as its own named entry; mark one `recommended` per input
   and explain each score in `report.json`. Do not discard non-recommended usable sources.
4. Resolve the reference/import closure of each selected source (OpenAPI/Swagger `$ref`,
   proto `import`) and include the whole set — copying every referenced file for
   `local_path` sources, or listing every needed path for pinned sources. Copying only the
   root file while it `$ref`s siblings produces a source Lathe cannot load.
5. Write the matching draft source block for OpenAPI 3, Swagger 2, Proto, or GraphQL.

### `sources.yaml` field rules (verified against Lathe's validator)

- Exactly one backend block per source; setting a block that does not match `backend`
  is rejected by Lathe.
- OpenAPI 3: `openapi3.files: [...]` (non-empty, relative, no `..`).
- Swagger 2: `swagger.files: [...]`. Swagger 2 is a native backend; never silently
  convert it to OpenAPI 3.
- Proto: `proto.entries` (protoc entry files) **and** `proto.staging` (`from`/`to` map)
  are both required; `proto.import_roots` optional. Lathe's proto sync **requires
  `protoc` on PATH**, and its codegen only emits commands for RPCs carrying a
  `google.api.http` annotation — a proto tree with no such annotations yields zero
  commands, so report it as a gap rather than a confident source.
- GraphQL: `graphql.schema` is a **single** file path (not a list), plus a required
  explicit `graphql.expose` with at least one query and/or mutation — Lathe refuses to
  expose the whole schema. List every discovered query/mutation under `expose` and mark
  the policy for human confirmation. Do not synthesize `groups`/`output`/`selection`;
  those are human policy.
- `default_hostname`: extract from OpenAPI `servers[].url`, or Swagger `host`+`schemes`,
  when unambiguous. It wires the generated CLI's auth flow (`auth status --hostname`)
  out of the box; leave it unset and flag a gap when ambiguous.
- Postman-only input produces a converted OpenAPI 3 draft at medium confidence with a
  `postman-needs-conversion` gap — never a native-looking, high-confidence source.

## L2 — Allowlisted framework extractors

L2 statically analyzes source. It does not start the application, import application
modules, run build scripts, install dependencies, or execute repository code.

Initial allowlist, in priority order:

1. FastAPI / `APIRouter`
2. Spring `@RequestMapping`, `@GetMapping`, `@PostMapping`, and sibling annotations
3. NestJS `@Controller` and method decorators
4. Gin route registration
5. Echo route registration
6. Chi route registration
7. Express / Fastify routes, conservatively limited to statically resolvable registrations

Each extractor emits an honest OpenAPI 3 draft:

- Recover path and method when statically resolvable.
- Recover path parameters, which the route pattern itself evidences. Query parameters and
  request bodies are **not** recovered today — they are reported as `body` gaps rather
  than guessed, and recovering them is L3 work.
- Attach `x-lathe-confidence` to inferred operations or schemas.
- Attach `x-lathe-gaps` such as `auth`, `body`, `response`, or `dynamic-route`.
- Attach `x-lathe-source-file` naming the file each route came from. The draft exists to
  be reviewed, and a route with no provenance cannot be checked.
- Omit unresolved routes rather than inventing them; list omissions in `report.json`
  and `GAPS.md`.
- Match per file, never over a concatenation of the repo: it bounds memory, keeps each
  route's origin, and stops a pattern from matching across a file boundary into a route
  that exists nowhere. A file-count cap that truncates the scan is reported as a
  `scan-truncated` gap rather than passed off as full coverage.

L2 runs only when L1 finds no usable Lathe-native source for that input. A framework
extractor never overrides a valid existing spec.

## L3 — Experimental

- Additional framework or language extractors.
- Cross-file or data-flow inference beyond statically obvious registrations.
- Any extractor with high false-positive risk.

L3 output is opt-in and never presented as complete.

## Confidence

Confidence answers "will Lathe actually generate commands from this?", not merely "does
the file parse?". A draft that would emit zero commands (proto with no `google.api.http`,
graphql whose `expose` matches nothing) is downgraded to a gap.

- `high`: parsed existing Lathe-native source that would emit at least one command.
- `medium`: statically extracted route plus evidenced parameter/schema details.
- `low`: partial static extraction with material gaps.
- Confidence is per candidate and per extracted operation, not one blanket claim for the
  repository.

## Acceptance

- One repo/zip with OpenAPI 3 / Swagger 2 → valid matching source.
- One repo/zip with Proto (with `google.api.http`) → valid source with usable entries,
  staging, and import roots; annotation-free proto → gap, not a confident source.
- One repo/zip with GraphQL SDL → valid source with an explicit discovered expose policy
  and a confirmation gap.
- Monorepo with several APIs → several named sources in one `sources.yaml`, none dropped.
- Multiple repos/zips → one merged `sources.yaml` aggregating all modules; `--merge`
  preserves foreign and hand-edited sources.
- A source in a git repo with an immutable ref → `repo_url` + `pinned_tag`; otherwise
  `local_path`. Never a fabricated origin.
- Multi-file spec with `$ref`/`import` → the full closure is included, and Lathe loads it.
- Vendored/dependency specs under ignored trees → not selected as sources.
- Postman-only tree → converted draft at medium confidence with its conversion gap.
- Re-running `--merge` over the same inputs → byte-identical `sources.yaml`, no duplicates.
- `--merge` after a human trims `graphql.expose` → the trim survives, with a gap saying so.
- `--merge` when the trimmed expose no longer matches the schema → entry untouched,
  blocking gap, exit 2. No operation is exposed that was not already approved.
- `--merge` over an entry carrying `groups`/`output`/`selection` → those survive, and only
  for the same provenance: a different API that reuses the freed name inherits nothing.
- `--merge` over an entry whose `graphql.expose` is empty → entry untouched, blocking gap,
  exit 2; never counted as a usable source.
- Scan A, then B, then A again → A's entry is updated in place, never duplicated.
- Any rescan, including one driven by a corrupted `report.json` → nothing under `--out` is
  deleted except the rewritten `sources.yaml`.
- `--merge` whose input cannot be scanned, or whose source turned blocking → that input's
  existing entries survive untouched with a `source-kept` gap, exit 2. A mistyped path
  never costs the user their manifest.
- `--merge` into an `--out` whose `report.json` predates source provenance while the
  manifest is non-empty → refused before anything is written: ownership is not
  reconstructible, and carrying on would append duplicates. Rebuilding without `--merge`
  is the stated way out.
- Only-blocking result (spec with no operations, unresolvable `$ref`) → exit 2, no
  `sources.yaml`, and the blocking gap present in `report.json` and `GAPS.md`.
- Allowlisted framework without a usable native source → parseable OpenAPI 3 draft with
  paths plus explicit confidence and gaps.
- Empty or unsupported inputs → exit 2; no fake OpenAPI.
- Re-run into a non-empty `--out` → refused unless `--force` or `--merge`.
- Deterministic: identical inputs produce byte-identical `sources.yaml`.
- Single binary, one command, offline, scanned inputs unchanged.

## Non-goals

- Source upload / SaaS pipeline
- Arbitrary-language full API recovery
- Running target services or executing scanned repository code
- Binary CLI generation or marketplace
- Coupling scan success to Lathe releases

## Relation to Lathe PMF

- The common real-world shape is one product with one CLI but many modules spread across
  many repos. L1 turns that scattered reality into one aggregated `sources.yaml`.
- L1 solves the case where specs exist but are buried, duplicated, or disconnected from
  the build. L2 is the cold-start path for allowlisted framework repos without a spec.
- Default path: local scan of one or more repos → human confirms sources, origins, expose
  policy, and gaps → local Lathe generates the aggregate CLI.
- Cloud is optional later and only for authorized spec/report artifacts, never source trees.
