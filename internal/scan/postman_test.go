package scan

import (
	"encoding/json"
	"path/filepath"
	"testing"
)

const postmanCollection = `{
  "info": { "name": "Acme API", "_postman_id": "abc-123",
            "schema": "https://schema.getpostman.com/json/collection/v2.1.0/collection.json" },
  "item": [
    { "name": "Users", "item": [
      { "name": "Get user", "request": { "method": "GET",
        "url": { "raw": "{{base}}/users/:id", "path": ["users", ":id"] } } },
      { "name": "Create", "request": { "method": "POST",
        "url": { "raw": "https://api.acme.com/users?x=1", "path": ["users"] } } }
    ]},
    { "name": "Health", "request": { "method": "GET", "url": "https://api.acme.com/health" } }
  ]
}`

func TestPostmanURLPath(t *testing.T) {
	cases := []struct{ raw, want string }{
		{`{"path":["users",":id"]}`, "/users/{id}"},
		{`{"path":["orders","{{orderId}}"]}`, "/orders/{orderId}"},
		{`"https://api.acme.com/health?x=1"`, "/health"},
		{`"https://api.acme.com/v1/pets"`, "/v1/pets"},
	}
	for _, c := range cases {
		if got := postmanURLPath(json.RawMessage(c.raw)); got != c.want {
			t.Errorf("postmanURLPath(%s) = %q, want %q", c.raw, got, c.want)
		}
	}
}

func TestIsPostmanCollection(t *testing.T) {
	if !isPostmanCollection([]byte(postmanCollection)) {
		t.Error("valid collection not detected")
	}
	if isPostmanCollection([]byte(`{"openapi":"3.0.0"}`)) {
		t.Error("openapi doc wrongly detected as postman")
	}
}

func TestExecutePostman(t *testing.T) {
	in := inputDir(t, "api.postman_collection.json", postmanCollection)
	out := t.TempDir()
	if err := Execute(Options{Inputs: []string{in}, Out: out}); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	s := firstSource(t, filepath.Join(out, sourcesFileName))
	if s["backend"] != "openapi3" {
		t.Fatalf("backend = %v", s["backend"])
	}
	rep := readReport(t, out).Sources[0]
	if rep.Extractor != "postman" {
		t.Errorf("extractor = %q, want postman", rep.Extractor)
	}
	if rep.WouldEmitCommands != 3 { // users/{id} GET, users POST, health GET
		t.Errorf("wouldEmit = %d, want 3", rep.WouldEmitCommands)
	}
}

// Postman (L1 artifact) is preferred over guessing routes from source (L2).
func TestPostmanSuppressesL2(t *testing.T) {
	in := t.TempDir()
	writeFile(t, in, "api.postman_collection.json", postmanCollection)
	writeFile(t, in, "app.py", fastAPISrc) // also FastAPI routes present

	out := t.TempDir()
	if err := Execute(Options{Inputs: []string{in}, Out: out}); err != nil {
		t.Fatal(err)
	}
	for _, s := range readReport(t, out).Sources {
		if s.Level == "L2" {
			t.Errorf("L2 ran despite a usable Postman source: %+v", s)
		}
	}
}
