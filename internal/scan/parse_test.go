package scan

import "testing"

const specOpenAPI = `openapi: 3.0.3
info:
  title: Billing API
  version: 1.0.0
servers:
  - url: https://api.acme.com/v1
paths:
  /invoices:
    get:
      responses:
        "200": {description: ok}
    post:
      responses:
        "200": {description: ok}
  /invoices/{id}:
    get:
      responses:
        "200": {description: ok}
    parameters:
      - name: id
        in: path
components:
  schemas:
    Invoice:
      type: object
`

const specSwagger = `swagger: "2.0"
info:
  title: Legacy API
  version: 1.0.0
host: legacy.acme.com
paths:
  /things:
    get:
      responses:
        "200": {description: ok}
definitions:
  Thing:
    type: object
`

const specExternalRef = `openapi: 3.0.0
info:
  title: Ref API
paths:
  /x:
    get:
      responses:
        "200":
          description: ok
          content:
            application/json:
              schema:
                $ref: "./schemas/x.yaml#/X"
`

func TestParseOpenAPI3(t *testing.T) {
	p, err := parseSpec([]byte(specOpenAPI))
	if err != nil {
		t.Fatalf("parseSpec: %v", err)
	}
	if p == nil {
		t.Fatal("expected openapi3 spec, got nil")
	}
	if p.format != "openapi3" {
		t.Errorf("format = %q, want openapi3", p.format)
	}
	if p.title != "Billing API" {
		t.Errorf("title = %q", p.title)
	}
	// The "parameters" key under a path item must NOT count as an operation.
	if p.metrics.Operations != 3 {
		t.Errorf("operations = %d, want 3", p.metrics.Operations)
	}
	if p.wouldEmit != 3 {
		t.Errorf("wouldEmit = %d, want 3", p.wouldEmit)
	}
	if p.metrics.Paths != 2 {
		t.Errorf("paths = %d, want 2", p.metrics.Paths)
	}
	if p.metrics.Schemas != 1 {
		t.Errorf("schemas = %d, want 1", p.metrics.Schemas)
	}
	if p.hostname != "api.acme.com" {
		t.Errorf("hostname = %q, want api.acme.com", p.hostname)
	}
	if p.hasExtRefs {
		t.Error("hasExtRefs = true, want false")
	}
}

func TestParseSwagger2(t *testing.T) {
	p, err := parseSpec([]byte(specSwagger))
	if err != nil {
		t.Fatalf("parseSpec: %v", err)
	}
	if p == nil || p.format != "swagger" {
		t.Fatalf("expected swagger, got %+v", p)
	}
	if p.metrics.Operations != 1 || p.wouldEmit != 1 {
		t.Errorf("operations = %d, want 1", p.metrics.Operations)
	}
	if p.hostname != "legacy.acme.com" {
		t.Errorf("hostname = %q", p.hostname)
	}
}

func TestParseNonSpec(t *testing.T) {
	p, err := parseSpec([]byte("name: not-an-api\nvalue: 1\n"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p != nil {
		t.Errorf("expected nil for non-spec, got %+v", p)
	}
}

func TestParseInvalidYAML(t *testing.T) {
	_, err := parseSpec([]byte("openapi: 3.0.0\n  bad: : :\n"))
	if err == nil {
		t.Error("expected parse error for malformed yaml")
	}
}

func TestExternalRefDetection(t *testing.T) {
	p, err := parseSpec([]byte(specExternalRef))
	if err != nil {
		t.Fatalf("parseSpec: %v", err)
	}
	if !p.hasExtRefs {
		t.Error("hasExtRefs = false, want true for external $ref")
	}
}

func TestInternalRefIsNotExternal(t *testing.T) {
	spec := `openapi: 3.0.0
info: {title: T}
paths:
  /x:
    get:
      responses:
        "200":
          description: ok
          content:
            application/json:
              schema:
                $ref: "#/components/schemas/X"
components:
  schemas:
    X: {type: object}
`
	p, _ := parseSpec([]byte(spec))
	if p.hasExtRefs {
		t.Error("internal #/ ref must not be flagged external")
	}
}

func TestHostFromURL(t *testing.T) {
	cases := map[string]string{
		"https://api.acme.com/v1": "api.acme.com",
		"http://localhost:8080":   "localhost:8080",
		"":                        "",
		"/relative/only":          "",
	}
	for in, want := range cases {
		if got := hostFromURL(in); got != want {
			t.Errorf("hostFromURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestContentHashStable(t *testing.T) {
	a, _ := parseSpec([]byte(specOpenAPI))
	b, _ := parseSpec([]byte(specOpenAPI))
	if a.contentHash != b.contentHash || a.contentHash == "" {
		t.Errorf("content hash not stable: %q vs %q", a.contentHash, b.contentHash)
	}
}
