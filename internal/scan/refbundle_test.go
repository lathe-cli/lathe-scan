package scan

import (
	"gopkg.in/yaml.v3"
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

// A path-item (non-schema) external ref must NOT be hoisted into schemas; it is
// reported missing so the source falls back to a blocking gap.
func TestBundleSkipsNonSchemaRef(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "openapi.yaml", `openapi: 3.0.3
info: { title: T }
paths:
  /users:
    $ref: "./paths/users.yaml"
`)
	writeFile(t, dir, "paths/users.yaml", "get:\n  responses:\n    \"200\": { description: ok }\n")

	out, _, missing, err := bundleSpec(filepath.Join(dir, "openapi.yaml"), "openapi3", dir)
	if err != nil {
		t.Fatalf("bundleSpec: %v", err)
	}
	if len(missing) == 0 {
		t.Errorf("non-schema external ref should be reported missing, not hoisted; bundle:\n%s", out)
	}
	if strings.Contains(string(out), "#/components/schemas/users") {
		t.Errorf("path-item ref was wrongly rewritten into schemas:\n%s", out)
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

	// Unbundleable → blocking ref gap, so no source is emitted; the gap is the
	// whole result and has to survive at the report's top level.
	err := Execute(Options{Inputs: []string{in}, Out: out})
	if err == nil {
		t.Fatal("expected ErrNoSources: a spec with an unresolvable $ref is not usable")
	}
	rep := readReport(t, out)
	if len(rep.Sources) != 0 {
		t.Errorf("unbundleable spec must not be emitted: %+v", rep.Sources)
	}
	if !hasGap(rep.Gaps, gapRefUnresolved, true) {
		t.Errorf("expected blocking ref-unresolved gap, got %+v", rep.Gaps)
	}
}

// A schema hoisted out of an external file carries its own "#/..." refs with it.
// Those are relative to the file they came from, so once the fragment lands in
// the root document they must be re-resolved against that file — not silently
// re-pointed at whatever the root happens to call the same name. Here the root's
// Address is a string and the external one is an object: getting this wrong
// hands the caller a spec that type-checks and means something else.
func TestBundleDoesNotShadowExternalSchemaWithRootName(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "openapi.yaml", `openapi: 3.0.3
info: { title: Shadow Probe, version: 1.0.0 }
paths:
  /users:
    get:
      responses:
        "200":
          description: ok
          content:
            application/json:
              schema:
                $ref: "./schemas/user.yaml#/components/schemas/User"
components:
  schemas:
    Address:
      type: string
      description: root document schema with the same name
`)
	writeFile(t, dir, "schemas/user.yaml", `components:
  schemas:
    User:
      type: object
      properties:
        address:
          $ref: "#/components/schemas/Address"
    Address:
      type: object
      properties:
        city: { type: string }
`)

	out, _, missing, err := bundleSpec(filepath.Join(dir, "openapi.yaml"), "openapi3", dir)
	if err != nil {
		t.Fatalf("bundleSpec: %v", err)
	}
	if len(missing) != 0 {
		t.Fatalf("unexpected missing refs: %v", missing)
	}

	var doc struct {
		Components struct {
			Schemas map[string]struct {
				Type       string `yaml:"type"`
				Properties map[string]struct {
					Ref string `yaml:"$ref"`
				} `yaml:"properties"`
			} `yaml:"schemas"`
		} `yaml:"components"`
	}
	if err := yaml.Unmarshal(out, &doc); err != nil {
		t.Fatalf("bundled spec does not parse: %v", err)
	}
	ref := doc.Components.Schemas["User"].Properties["address"].Ref
	if ref == "" {
		t.Fatalf("User.address lost its $ref:\n%s", out)
	}
	target := doc.Components.Schemas[strings.TrimPrefix(ref, "#/components/schemas/")]
	if target.Type != "object" {
		t.Errorf("User.address resolved to %q, want the external object schema; ref=%s\n%s", target.Type, ref, out)
	}
	if doc.Components.Schemas["Address"].Type != "string" {
		t.Errorf("the root document's own Address was overwritten:\n%s", out)
	}
}

// Identical inputs must produce identical bytes. Hoisted names are assigned in
// walk order, so an unordered map walk hands the unsuffixed name to a different
// schema from run to run.
func TestBundleSpecIsDeterministic(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "openapi.yaml", `openapi: 3.0.3
info: { title: Determinism Probe, version: 1.0.0 }
paths:
  /a:
    get:
      responses:
        "200":
          description: ok
          content:
            application/json:
              schema: { $ref: "./a.yaml#/components/schemas/Error" }
  /b:
    get:
      responses:
        "200":
          description: ok
          content:
            application/json:
              schema: { $ref: "./b.yaml#/components/schemas/Error" }
`)
	writeFile(t, dir, "a.yaml", "components:\n  schemas:\n    Error:\n      type: object\n      properties:\n        from_a: { type: string }\n")
	writeFile(t, dir, "b.yaml", "components:\n  schemas:\n    Error:\n      type: object\n      properties:\n        from_b: { type: string }\n")

	var first string
	for i := range 50 {
		out, _, missing, err := bundleSpec(filepath.Join(dir, "openapi.yaml"), "openapi3", dir)
		if err != nil || len(missing) != 0 {
			t.Fatalf("bundleSpec: %v missing=%v", err, missing)
		}
		if i == 0 {
			first = string(out)
			continue
		}
		if string(out) != first {
			t.Fatalf("bundling is not deterministic; run %d differs:\n--- first ---\n%s\n--- run %d ---\n%s", i, first, i, out)
		}
	}
}
