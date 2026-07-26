package scan

import (
	"archive/zip"
	"os"
	"path/filepath"
	"strings"
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

// A hostile archive is refused outright rather than partly extracted: scanning
// the innocent remainder would report success over a subset the user never chose,
// with nothing on the record saying entries were dropped.
func TestExtractZipRejectsTraversal(t *testing.T) {
	zp := makeZip(t, map[string]string{
		"svc/openapi.yaml": specOpenAPI,
		"../evil.txt":      "pwned", // Zip Slip attempt
	})
	dir, cleanup, err := extractZip(zp)
	if err == nil {
		cleanup()
		t.Fatal("extractZip accepted an archive containing a traversal entry")
	}
	if !strings.Contains(err.Error(), "escapes the archive root") {
		t.Errorf("error does not say why the archive was refused: %v", err)
	}
	if dir != "" {
		t.Errorf("a refused archive must not hand back an extraction dir, got %q", dir)
	}
	// Nothing may have escaped, whether or not extraction stopped early.
	if _, err := os.Stat(filepath.Join(filepath.Dir(dir), "evil.txt")); err == nil {
		t.Error("Zip Slip: evil.txt written outside extraction dir")
	}
}

// A zip with no spec falls through to L2; the source name must come from the
// zip filename, not the random extraction dir (deterministic, --merge-safe).
func TestExecuteZipL2NameFromArchive(t *testing.T) {
	run := func() string {
		zp := makeZip(t, map[string]string{"app/main.py": fastAPISrc})
		out := t.TempDir()
		if err := Execute(Options{Inputs: []string{zp}, Out: out}); err != nil {
			t.Fatal(err)
		}
		for k := range readSources(t, filepath.Join(out, sourcesFileName)) {
			return k
		}
		return ""
	}
	n1, n2 := run(), run()
	// makeZip names the archive in.zip -> source name "in", the same every run.
	if n1 != "in" {
		t.Errorf("zip L2 source name = %q, want in (from archive filename)", n1)
	}
	if n1 != n2 {
		t.Errorf("zip L2 source name not deterministic: %q vs %q", n1, n2)
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
