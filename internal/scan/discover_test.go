package scan

import (
	"os"
	"path/filepath"
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
