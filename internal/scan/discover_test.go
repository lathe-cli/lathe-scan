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

func TestDiscoverSkipsIgnoredDirs(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "openapi.yaml", specOpenAPI)
	writeFile(t, root, "node_modules/dep/openapi.yaml", specOpenAPI) // must be skipped
	writeFile(t, root, "vendor/x/swagger.json", specSwagger)         // must be skipped
	writeFile(t, root, ".git/openapi.yaml", specOpenAPI)             // must be skipped

	got := discover(root)
	if len(got) != 1 {
		t.Fatalf("discover found %d files, want 1: %v", len(got), got)
	}
	if filepath.Base(got[0]) != "openapi.yaml" || filepath.Dir(got[0]) != root {
		t.Errorf("discovered wrong file: %s", got[0])
	}
}

func TestDiscoverSkipsTestAndSampleDirs(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "openapi.yaml", specOpenAPI) // the real one
	for _, d := range []string{"samples", "sample", "test", "tests", "__tests__", "fixtures", "fixture", "e2e", "third_party", "generated"} {
		writeFile(t, root, d+"/openapi.yaml", specOpenAPI) // scaffolding — must be skipped
	}
	got := discover(root)
	if len(got) != 1 || filepath.Dir(got[0]) != root {
		t.Fatalf("want only the root spec, got %d: %v", len(got), got)
	}
}

// Same API in json+yaml or copied to a second dir → one canonical source, even
// though bytes (and content hashes) differ.
func TestDedupBySignature(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "openapi.yaml", specOpenAPI)
	// Same operations, different bytes (title + server changed).
	variant := strings.NewReplacer("Billing API", "Billing API v2", "api.acme.com", "api2.acme.com").Replace(specOpenAPI)
	writeFile(t, root, "docs/openapi.yaml", variant)

	cands, parsed := parseCandidates(discover(root), root)
	dedupCandidates(cands, parsed)
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
	cands, parsed := parseCandidates(discover(root), root)
	dedupCandidates(cands, parsed)
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
	writeFile(t, root, "openapi.yaml", specOpenAPI)
	writeFile(t, root, "docs/openapi.json", specOpenAPI) // identical content, different path

	files := discover(root)
	cands, parsed := parseCandidates(files, root)
	dedupCandidates(cands, parsed)

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
		"Billing API":    "billing-api",
		"  Acme/v2  ":    "acme-v2",
		"___":            "",
		"petStore-2000!": "petstore-2000",
	}
	for in, want := range cases {
		if got := sanitizeName(in); got != want {
			t.Errorf("sanitizeName(%q) = %q, want %q", in, got, want)
		}
	}
}
