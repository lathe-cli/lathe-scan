package scan

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A spec file that is a symlink to a file outside the scanned tree must not be
// read, and above all must not have its contents copied into --out, where they
// would be committed alongside sources.yaml. The lexical path looks contained;
// only the resolved one tells the truth.
func TestScanRefusesSymlinkedCandidateOutsideRoot(t *testing.T) {
	base := t.TempDir()
	outside, in := filepath.Join(base, "outside"), filepath.Join(base, "in")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(in, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, outside, "spec.yaml", strings.Replace(specOpenAPI, "Billing API", "Escaped Secret", 1))
	if err := os.Symlink(filepath.Join(outside, "spec.yaml"), filepath.Join(in, "openapi.yaml")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	out := t.TempDir()
	err := Execute(Options{Inputs: []string{in}, Out: out})
	if err == nil {
		t.Error("a tree whose only candidate escapes the root must not produce a source")
	}

	if leaked := grepTree(t, out, "Escaped Secret"); leaked != "" {
		t.Errorf("content from outside the input root was copied into --out: %s", leaked)
	}
	rep := readReport(t, out)
	if len(rep.Sources) != 0 {
		t.Errorf("escaping candidate was emitted as a source: %+v", rep.Sources)
	}
	// Refused, not silently dropped: the candidate is on the record with a reason.
	var found bool
	for _, in := range rep.Inputs {
		for _, c := range in.Candidates {
			if strings.Contains(c.Error, "outside the input root") {
				found = true
			}
		}
	}
	if !found {
		t.Errorf("refusal was not recorded in candidates: %+v", rep.Inputs)
	}
}

// $ref closure follows the same rule as the candidate itself: the target being
// lexically under the root is not enough when a symlinked directory redirects it.
func TestBundleRefusesSymlinkedRefOutsideRoot(t *testing.T) {
	base := t.TempDir()
	outside, in := filepath.Join(base, "outside"), filepath.Join(base, "in")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(in, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, outside, "address.yaml",
		"components:\n  schemas:\n    Address:\n      type: object\n      properties:\n        secret_shape: { type: string }\n")
	if err := os.Symlink(outside, filepath.Join(in, "escape")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	writeFile(t, in, "openapi.yaml", `openapi: 3.0.3
info: { title: Escape Probe, version: 1.0.0 }
paths:
  /address:
    get:
      responses:
        "200":
          description: ok
          content:
            application/json:
              schema:
                $ref: "./escape/address.yaml#/components/schemas/Address"
`)

	out, _, missing, err := bundleSpec(filepath.Join(in, "openapi.yaml"), "openapi3", in)
	if err != nil {
		t.Fatalf("bundleSpec: %v", err)
	}
	if len(missing) == 0 {
		t.Errorf("a $ref escaping the root must be reported missing, not inlined:\n%s", out)
	}
	if strings.Contains(string(out), "secret_shape") {
		t.Errorf("content from outside the root was inlined:\n%s", out)
	}
}

// Writing must respect the boundary too, and checking only the final path is not
// enough: an existing symlinked parent makes MkdirAll succeed and the write land
// outside --out entirely.
func TestWriteUnderRefusesSymlinkedParent(t *testing.T) {
	base := t.TempDir()
	dest, outside := filepath.Join(base, "out"), filepath.Join(base, "elsewhere")
	if err := os.MkdirAll(filepath.Join(dest), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dest, "schemas")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	err := writeUnder(dest, "schemas/api.yaml", []byte("leaked"))
	if err == nil {
		t.Fatal("writeUnder followed a symlinked parent out of the destination")
	}
	if !strings.Contains(err.Error(), "symlink") {
		t.Errorf("error does not name the cause: %v", err)
	}
	if _, err := os.Stat(filepath.Join(outside, "api.yaml")); err == nil {
		t.Error("file was written outside the destination directory")
	}
}

// A dangling symlink reads as "absent" to Stat, so a check built on Stat would
// clear the way for a write that then follows it.
func TestWriteUnderRefusesDanglingSymlinkTarget(t *testing.T) {
	dest := t.TempDir()
	if err := os.Symlink(filepath.Join(dest, "nonexistent"), filepath.Join(dest, "openapi.yaml")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := writeUnder(dest, "openapi.yaml", []byte("x")); err == nil {
		t.Error("writeUnder wrote through a dangling symlink")
	}
}

func grepTree(t *testing.T, root, needle string) string {
	t.Helper()
	var hit string
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || hit != "" {
			return nil //nolint:nilerr // a walk error just means nothing to inspect here
		}
		data, rerr := os.ReadFile(path)
		if rerr == nil && strings.Contains(string(data), needle) {
			hit = path
		}
		return nil
	})
	return hit
}
