package scan

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// "Absent" and "unreadable" are different states. A missing report.json means a
// hand-written manifest, which --merge handles by carrying entries as foreign.
// A damaged one means ownership is unrecoverable: treating those entries as
// foreign would append a duplicate beside every source on the next scan, which
// is the loss --merge exists to prevent.
func TestMergeRefusesDamagedReport(t *testing.T) {
	in := inputDir(t, "openapi.yaml", specOpenAPI)
	out := t.TempDir()
	if err := Execute(Options{Inputs: []string{in}, Out: out}); err != nil {
		t.Fatalf("seed run: %v", err)
	}
	sourcesPath := filepath.Join(out, sourcesFileName)
	before, err := os.ReadFile(sourcesPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(out, reportFileName), []byte("{ truncated"), 0o644); err != nil {
		t.Fatal(err)
	}

	err = Execute(Options{Inputs: []string{in}, Out: out, Merge: true})
	if err == nil {
		t.Fatal("--merge accepted a damaged report.json")
	}
	if !strings.Contains(err.Error(), reportFileName) {
		t.Errorf("error does not name the damaged file: %v", err)
	}
	after, rerr := os.ReadFile(sourcesPath)
	if rerr != nil {
		t.Fatal(rerr)
	}
	if string(after) != string(before) {
		t.Errorf("a refused --merge rewrote the manifest:\n%s", after)
	}
}

// With no manifest to own, a damaged report has nothing to explain and must not
// block the run.
func TestMergeToleratesDamagedReportWithoutManifest(t *testing.T) {
	in := inputDir(t, "openapi.yaml", specOpenAPI)
	out := t.TempDir()
	if err := os.WriteFile(filepath.Join(out, reportFileName), []byte("{ truncated"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Execute(Options{Inputs: []string{in}, Out: out, Merge: true}); err != nil {
		t.Fatalf("Execute: %v", err)
	}
}

// A trimmed graphql.expose is an approval decision. Restoring the full
// discovered surface would publish operations the user deliberately removed, so
// a subset must stay a subset — including when the manifest lists an operation
// twice, which makes a length comparison read as "not trimmed".
func TestMergeKeepsTrimmedExposeWithDuplicateEntries(t *testing.T) {
	in := inputDir(t, "schema.graphql", "type Query { alpha: String beta: String }\n")
	out := t.TempDir()
	if err := Execute(Options{Inputs: []string{in}, Out: out}); err != nil {
		t.Fatalf("seed run: %v", err)
	}

	// The user trims to alpha, and lists it twice: two entries, two discovered
	// operations, equal counts — but a strictly narrower surface.
	sourcesPath := filepath.Join(out, sourcesFileName)
	data, err := os.ReadFile(sourcesPath)
	if err != nil {
		t.Fatal(err)
	}
	edited := strings.Replace(string(data),
		"queries:\n                    - alpha\n                    - beta",
		"queries:\n                    - alpha\n                    - alpha", 1)
	if edited == string(data) {
		t.Fatalf("test fixture did not match the emitted manifest:\n%s", data)
	}
	if err := os.WriteFile(sourcesPath, []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := Execute(Options{Inputs: []string{in}, Out: out, Merge: true}); err != nil {
		t.Fatalf("merge run: %v", err)
	}
	merged, err := os.ReadFile(sourcesPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(merged), "beta") {
		t.Errorf("merge re-exposed an operation the user had removed:\n%s", merged)
	}
}

// A source name is also a directory name under --out. Handing a new source a
// name that a foreign entry already points at overwrites data this run did not
// create, which report.json's own contract forbids treating as removable.
func TestNewSourceDoesNotOverwriteForeignDirectory(t *testing.T) {
	in := inputDir(t, "openapi.yaml", specOpenAPI)
	out := t.TempDir()

	// A foreign entry pointing at billing_api, plus the directory it references.
	manifest := "sources:\n    foreign:\n        local_path: billing_api\n        backend: openapi3\n        openapi3:\n            files:\n                - openapi.yaml\n"
	if err := os.WriteFile(filepath.Join(out, sourcesFileName), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(out, "billing_api"), 0o755); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(out, "billing_api", "openapi.yaml")
	if err := os.WriteFile(sentinel, []byte("foreign-owned content\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := Execute(Options{Inputs: []string{in}, Out: out, Merge: true}); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	data, err := os.ReadFile(sentinel)
	if err != nil {
		t.Fatalf("foreign directory disappeared: %v", err)
	}
	if string(data) != "foreign-owned content\n" {
		t.Errorf("foreign entry's data was overwritten:\n%s", data)
	}
	srcs := readSources(t, filepath.Join(out, sourcesFileName))
	if len(srcs) != 2 {
		t.Errorf("want the foreign entry plus the new source, got %v", srcs)
	}
}
