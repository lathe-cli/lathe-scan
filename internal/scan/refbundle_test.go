package scan

import (
	"path/filepath"
	"strings"
	"testing"
)

const splitMain = `openapi: 3.0.3
info: { title: Split API, version: 1.0.0 }
paths:
  /users/{id}:
    get:
      responses:
        "200":
          description: ok
          content:
            application/json:
              schema:
                $ref: "./schemas/user.yaml#/components/schemas/User"
`

const splitUser = `components:
  schemas:
    User:
      type: object
      properties:
        id: { type: integer }
        manager:
          $ref: "./user.yaml#/components/schemas/User"
`

func TestBundleSpecInlinesAndRewrites(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "openapi.yaml", splitMain)
	writeFile(t, dir, "schemas/user.yaml", splitUser)

	out, files, missing, err := bundleSpec(filepath.Join(dir, "openapi.yaml"), "openapi3", dir)
	if err != nil {
		t.Fatalf("bundleSpec: %v", err)
	}
	if len(missing) != 0 {
		t.Fatalf("unexpected missing refs: %v", missing)
	}
	if len(files) != 2 {
		t.Errorf("closure files = %d, want 2", len(files))
	}
	s := string(out)
	if strings.Contains(s, "user.yaml") {
		t.Errorf("external ref not rewritten:\n%s", s)
	}
	if !strings.Contains(s, "#/components/schemas/User") {
		t.Errorf("internal ref missing:\n%s", s)
	}
	p, err := parseSpec(out)
	if err != nil || p == nil || p.metrics.Operations != 1 {
		t.Errorf("bundled spec did not re-parse to 1 op: %+v (%v)", p, err)
	}
	if p.hasExtRefs {
		t.Error("bundled spec still has external refs")
	}
}

func TestExtractFragment(t *testing.T) {
	doc := map[string]any{
		"components": map[string]any{
			"schemas": map[string]any{"User": map[string]any{"type": "object"}},
		},
	}
	if _, ok := extractFragment(doc, "/components/schemas/User"); !ok {
		t.Error("failed to extract existing fragment")
	}
	if _, ok := extractFragment(doc, "/components/schemas/Missing"); ok {
		t.Error("extracted a nonexistent fragment")
	}
	if got, ok := extractFragment(doc, ""); !ok || got == nil {
		t.Error("empty fragment should return whole doc")
	}
}

func TestExecuteBundledSource(t *testing.T) {
	in := t.TempDir()
	writeFile(t, in, "openapi.yaml", splitMain)
	writeFile(t, in, "schemas/user.yaml", splitUser)

	out := t.TempDir()
	if err := Execute(Options{Inputs: []string{in}, Out: out}); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	rep := readReport(t, out).Sources[0]
	if rep.Confidence != confHigh {
		t.Errorf("confidence = %q, want high", rep.Confidence)
	}
	if !hasGap(rep.Gaps, gapRefBundled, false) {
		t.Errorf("expected advisory ref-closure-bundled gap, got %+v", rep.Gaps)
	}
	// Bundled artifact is synthesized → local_path, never a foreign-ref file copy.
	s := firstSource(t, filepath.Join(out, sourcesFileName))
	if s["local_path"] == nil {
		t.Errorf("bundled source should be local_path: %v", s)
	}
}

func TestExecuteMissingRefFallsBack(t *testing.T) {
	spec := `openapi: 3.0.3
info: { title: Broken }
paths:
  /x:
    get:
      responses:
        "200":
          description: ok
          content:
            application/json:
              schema:
                $ref: "./nonexistent.yaml#/components/schemas/Nope"
`
	in := t.TempDir()
	writeFile(t, in, "openapi.yaml", spec)
	out := t.TempDir()
	if err := Execute(Options{Inputs: []string{in}, Out: out}); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	rep := readReport(t, out).Sources[0]
	// Unbundleable → blocking ref gap, low confidence, no bundled gap.
	if !hasGap(rep.Gaps, gapRefUnresolved, true) {
		t.Errorf("expected blocking ref-unresolved gap, got %+v", rep.Gaps)
	}
	if rep.Confidence != confLow {
		t.Errorf("confidence = %q, want low", rep.Confidence)
	}
}
