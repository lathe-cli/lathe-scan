package scan

import (
	"path/filepath"
	"strings"
	"testing"
)

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

// default_hostname is only extracted when the servers agree. Taking servers[0]
// would let list order decide where authenticated commands are sent, and the
// first entry is conventionally production.
func TestHostnameOnlyWhenUnambiguous(t *testing.T) {
	cases := []struct {
		name, servers, wantHost string
		wantCandidates          int
	}{
		{"single", "  - url: https://api.acme.com/v1\n", "api.acme.com", 0},
		// Several URLs, one host: still unambiguous.
		{"same host", "  - url: https://api.acme.com/v1\n  - url: https://api.acme.com/v2\n", "api.acme.com", 0},
		{"different hosts", "  - url: https://prod.acme.com/v1\n  - url: https://sandbox.acme.com/v1\n", "", 2},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			spec := "openapi: 3.0.3\ninfo: { title: T, version: \"1\" }\nservers:\n" + c.servers +
				"paths:\n  /x:\n    get:\n      responses:\n        \"200\": { description: ok }\n"
			p, err := parseSpec([]byte(spec))
			if err != nil || p == nil {
				t.Fatalf("parseSpec: %v", err)
			}
			if p.hostname != c.wantHost {
				t.Errorf("hostname = %q, want %q", p.hostname, c.wantHost)
			}
			if len(p.hostCandidates) != c.wantCandidates {
				t.Errorf("hostCandidates = %v, want %d", p.hostCandidates, c.wantCandidates)
			}
		})
	}
}

// Leaving the field empty is only half the job: the user needs to know there was
// a choice, and what the options were.
func TestAmbiguousHostGapNamesCandidates(t *testing.T) {
	in := inputDir(t, "openapi.yaml", `openapi: 3.0.3
info: { title: Host Probe, version: "1" }
servers:
  - url: https://prod.acme.com/v1
  - url: https://sandbox.acme.com/v1
paths:
  /health:
    get:
      responses:
        "200": { description: ok }
`)
	out := t.TempDir()
	if err := Execute(Options{Inputs: []string{in}, Out: out}); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if s := firstSource(t, filepath.Join(out, sourcesFileName)); s["default_hostname"] != nil {
		t.Errorf("ambiguous servers must not yield a default_hostname: %v", s["default_hostname"])
	}
	rep := readReport(t, out).Sources[0]
	var msg string
	for _, g := range rep.Gaps {
		if g.Kind == gapAmbiguousHost {
			msg = g.Message
		}
	}
	if !strings.Contains(msg, "prod.acme.com") || !strings.Contains(msg, "sandbox.acme.com") {
		t.Errorf("gap should name the candidate hostnames, got %q", msg)
	}
}
