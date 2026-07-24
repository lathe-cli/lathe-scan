# Contributing to lathe-scan

Thanks for your interest in improving `lathe-scan`.

## Scope

`lathe-scan` discovers API specs and emits a draft Lathe `sources.yaml`. It is
deliberately narrow:

- It is **offline and read-only**. It must never make network calls, upload
  source, or modify a scanned input.
- It **never executes scanned code**. Framework extractors (L2) are static
  analysis only — no starting apps, importing modules, running builds, or
  installing dependencies.
- It **does not generate CLIs**. Code generation belongs to Lathe; `lathe-scan`
  produces inputs Lathe consumes.
- Emitted `sources.yaml` must load in Lathe's `sourceconfig` validator. Prefer
  reproducible `repo_url` + `pinned_tag` origins; fall back to `local_path`;
  never fabricate an origin.

Changes that move weight into runtime behavior, non-spec-driven output, or
online services are out of scope. See [docs/DESIGN.md](docs/DESIGN.md).

## Development

```sh
make check   # fmt-check, vet, lint, test — run before every PR
```

Requires Go 1.25+.

- Format with `gofmt` (`make fmt`).
- Wrap errors with context: `fmt.Errorf("...: %w", err)`.
- Add package-local `*_test.go` and `testdata/` fixtures for parse, selection,
  and emit behavior.
- Keep discovery deterministic — identical inputs must produce identical output.

## Commits & PRs

- Use [Conventional Commits](https://www.conventionalcommits.org/).
- Sign off every commit: `git commit -s`.
- Keep PRs focused and include the exact verification commands you ran.

## License

By contributing, you agree that your contributions are licensed under the
[MIT License](LICENSE).
