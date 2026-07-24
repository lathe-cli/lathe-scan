package scan

import (
	"archive/zip"
	"os"
	"path/filepath"
	"testing"
)

func makeZip(t *testing.T, entries map[string]string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "in.zip")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	w := zip.NewWriter(f)
	for name, content := range entries {
		fw, err := w.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fw.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestExtractZipRejectsTraversal(t *testing.T) {
	zp := makeZip(t, map[string]string{
		"svc/openapi.yaml": specOpenAPI,
		"../evil.txt":      "pwned", // Zip Slip attempt
	})
	dir, cleanup, err := extractZip(zp)
	if err != nil {
		t.Fatalf("extractZip: %v", err)
	}
	defer cleanup()

	if _, err := os.Stat(filepath.Join(dir, "svc", "openapi.yaml")); err != nil {
		t.Errorf("legit file missing: %v", err)
	}
	// The traversal entry must not have escaped the extraction dir.
	if _, err := os.Stat(filepath.Join(filepath.Dir(dir), "evil.txt")); err == nil {
		t.Error("Zip Slip: evil.txt written outside extraction dir")
	}
}

func TestExecuteZipInput(t *testing.T) {
	zp := makeZip(t, map[string]string{"svc/docs/openapi.yaml": specOpenAPI})
	out := t.TempDir()
	if err := Execute(Options{Inputs: []string{zp}, Out: out}); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	rep := readReport(t, out)
	if rep.Inputs[0].Kind != "zip" {
		t.Errorf("kind = %q, want zip", rep.Inputs[0].Kind)
	}
	s := firstSource(t, filepath.Join(out, sourcesFileName))
	// A zip is a snapshot: local_path, never a pinned repo.
	if s["local_path"] == nil || s["repo_url"] != nil {
		t.Errorf("zip source should be local_path only: %v", s)
	}
}
