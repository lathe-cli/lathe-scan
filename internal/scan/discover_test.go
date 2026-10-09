package scan

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, dir, rel, content string) string {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLooksLikeSpecFile(t *testing.T) {
	yes := []string{"openapi.yaml", "swagger.json", "api/openapi.yml", "docs/anything.yaml", "spec/foo.json"}
	no := []string{"readme.md", "config.yaml", "src/main.go", "package.json"}
	for _, p := range yes {
		if !looksLikeSpecFile(p) {
			t.Errorf("looksLikeSpecFile(%q) = false, want true", p)
		}
	}
	for _, p := range no {
		if looksLikeSpecFile(p) {
			t.Errorf("looksLikeSpecFile(%q) = true, want false", p)
		}
	}
}

// Specs are recognized by content, not name: petstore.yaml at the root and a
// JSON swagger under an unhinted directory must both compete, while ordinary
// YAML that merely mentions the word openapi stays out.
func TestSpecCandidateFilesProbesByContent(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "petstore.yaml", specOpenAPI)
	writeFile(t, root, "schemas/legacy.json", `{"swagger":"2.0","info":{"title":"Legacy"},"paths":{"/a":{"get":{"responses":{"200":{"description":"ok"}}}}}}`)
	writeFile(t, root, "config.yaml", "port: 8080\nnote: openapi is reviewed elsewhere\n")

	files := specCandidateFiles(indexFiles(root), root)
	if len(files) != 2 {
		t.Fatalf("candidates = %v, want petstore.yaml and schemas/legacy.json", files)
	}
	cands, _ := parseCandidates(files, root)
	if len(cands) != 2 {
		t.Fatalf("parsed candidates = %+v, want 2", cands)
	}
	for _, c := range cands {
		if !c.Parsed {
			t.Errorf("candidate %s failed to parse: %s", c.Path, c.Error)
		}
	}
}

func TestExecuteDiscoversSpecByContent(t *testing.T) {
	in := inputDir(t, "petstore.yaml", specOpenAPI)
	// Mentioning openapi in prose must not turn a config file into a candidate.
	writeFile(t, in, "ci.yaml", "jobs:\n  lint:\n    note: validate openapi elsewhere\n")
	out := t.TempDir()
	if err := Execute(Options{Inputs: []string{in}, Out: out}); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	srcs := readSources(t, filepath.Join(out, sourcesFileName))
	if len(srcs) != 1 {
		t.Fatalf("want 1 source from the unhinted spec, got %d: %v", len(srcs), srcs)
	}
	rep := readReport(t, out)
	if len(rep.Inputs) != 1 || len(rep.Inputs[0].Candidates) != 1 {
		t.Fatalf("candidates = %+v, want only petstore.yaml", rep.Inputs[0].Candidates)
	}
}

func TestDiscoverSkipsIgnoredDirs(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "openapi.yaml", specOpenAPI)
	writeFile(t, root, "node_modules/dep/openapi.yaml", specOpenAPI) // must be skipped
	writeFile(t, root, "vendor/x/swagger.json", specSwagger)         // must be skipped
	writeFile(t, root, ".git/openapi.yaml", specOpenAPI)             // must be skipped

	got := indexFiles(root).specs
	if len(got) != 1 {
		t.Fatalf("discover found %d files, want 1: %v", len(got), got)
	}
	if filepath.Base(got[0]) != "openapi.yaml" || filepath.Dir(got[0]) != root {
		t.Errorf("discovered wrong file: %s", got[0])
	}
}

func TestRepoRelativePathCanonicalizesEquivalentPaths(t *testing.T) {
	root := t.TempDir()
	spec := writeFile(t, root, "api/openapi.yaml", specOpenAPI)
	alias := filepath.Join(t.TempDir(), "repo")
	if err := os.Symlink(root, alias); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	if got := repoRelativePath(alias, spec); got != "api/openapi.yaml" {
		t.Fatalf("repoRelativePath(alias, spec) = %q, want api/openapi.yaml", got)
	}
}

func TestRepoRelativePathRefusesPathsOutsideRoot(t *testing.T) {
	root := t.TempDir()
	outside := writeFile(t, t.TempDir(), "openapi.yaml", specOpenAPI)

	if got := repoRelativePath(root, outside); got != "" {
		t.Fatalf("repoRelativePath(root, outside) = %q, want empty", got)
	}
}

func TestDiscoverSkipsTestAndSampleDirs(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "openapi.yaml", specOpenAPI) // the real one
	for _, d := range []string{"samples", "sample", "test", "tests", "__tests__", "fixtures", "fixture", "e2e", "third_party", "generated"} {
		writeFile(t, root, d+"/openapi.yaml", specOpenAPI) // scaffolding — must be skipped
	}
	got := indexFiles(root).specs
	if len(got) != 1 || filepath.Dir(got[0]) != root {
		t.Fatalf("want only the root spec, got %d: %v", len(got), got)
	}
}

// JVM package directories reuse fixture words as production namespace segments
// (org.springframework.samples, com.example). Under a src/main source root
// they are real code and must be indexed; the same names at the repo root stay
// excluded as scaffolding.
func TestDiscoverKeepsFixtureNamesUnderSrcMain(t *testing.T) {
	root := t.TempDir()
	controller := "package com.example.demo;\nimport org.springframework.web.bind.annotation.*;\n@RestController\npublic class Api {\n  @GetMapping(\"/x\")\n  public String x() { return \"x\"; }\n}\n"
	writeFile(t, root, "src/main/java/com/example/demo/Api.java", controller)
	writeFile(t, root, "examples/demo/Api.java", controller) // scaffolding — skipped
	idx := indexFiles(root)
	if len(idx.sources) != 1 || !strings.Contains(idx.sources[0], filepath.FromSlash("src/main")) {
		t.Fatalf("sources = %v, want only the src/main file", idx.sources)
	}
}

// A GraphQL schema that fails strict SDL validation fails the same way in
// Lathe. The refusal must surface as a blocking gap, not an empty result.
func TestExecuteInvalidGraphQLRaisesBlockingGap(t *testing.T) {
	in := inputDir(t, "schema.graphql", "type Query { a: String a: String }\n")
	out := t.TempDir()
	err := Execute(Options{Inputs: []string{in}, Out: out})
	var noSrc ErrNoSources
	if err == nil || !asNoSources(err, &noSrc) {
		t.Fatalf("want ErrNoSources, got %v", err)
	}
	if !hasGap(readReport(t, out).Gaps, gapParseError, true) {
		t.Errorf("expected blocking parse-error gap, got %+v", readReport(t, out).Gaps)
	}
}

// Copies in one location lineage remain one canonical source even though
// their bytes (and content hashes) differ.
func TestDedupBySignature(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "docs/v1/openapi.yaml", specOpenAPI)
	variant := strings.ReplaceAll(specOpenAPI, "description: ok", "description: success")
	writeFile(t, root, "docs/master/openapi.yaml", variant)

	cands, parsed := parseCandidates(indexFiles(root).specs, root)
	dedupCandidates(cands, parsed, "")
	nonDup := 0
	for _, c := range cands {
		if c.DuplicateOf == "" {
			nonDup++
		}
	}
	if nonDup != 1 {
		t.Errorf("same-API different-bytes specs should dedup to 1, got %d (%+v)", nonDup, cands)
	}
}

func TestDedupSignatureKeepsDistinctAPIs(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "a/openapi.yaml", specOpenAPI)
	writeFile(t, root, "b/openapi.yaml", strings.ReplaceAll(specOpenAPI, "invoices", "orders")) // different paths
	cands, parsed := parseCandidates(indexFiles(root).specs, root)
	dedupCandidates(cands, parsed, "")
	nonDup := 0
	for _, c := range cands {
		if c.DuplicateOf == "" {
			nonDup++
		}
	}
	if nonDup != 2 {
		t.Errorf("distinct APIs must not be deduped, got %d canonical", nonDup)
	}
}

func TestDedupByContentHash(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "docs/openapi.yaml", specOpenAPI)
	writeFile(t, root, "docs/openapi.json", specOpenAPI) // identical content, same lineage

	files := indexFiles(root).specs
	cands, parsed := parseCandidates(files, root)
	dedupCandidates(cands, parsed, "")

	nonDup := 0
	for _, c := range cands {
		if c.DuplicateOf == "" {
			nonDup++
		}
	}
	if nonDup != 1 {
		t.Errorf("expected 1 canonical candidate after dedup, got %d (%+v)", nonDup, cands)
	}
	if len(parsed) != 1 {
		t.Errorf("parsedByPath should retain only the canonical, got %d", len(parsed))
	}
}

func TestNameSanitize(t *testing.T) {
	cases := map[string]string{
		"Billing API":    "billing_api",
		"  Acme/v2  ":    "acme_v2",
		"___":            "",
		"petStore-2000!": "petstore_2000",
		"2000 Sensors":   "s_2000_sensors",
		"Type":           "s_type",
		"map":            "s_map",
		"Go API":         "go_api",
	}
	for in, want := range cases {
		if got := sanitizeName(in); got != want {
			t.Errorf("sanitizeName(%q) = %q, want %q", in, got, want)
		}
	}
}
