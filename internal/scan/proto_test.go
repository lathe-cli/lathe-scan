package scan

import (
	"path/filepath"
	"strings"
	"testing"
)

const protoWithHTTP = `syntax = "proto3";
package acme.v1;
import "google/api/annotations.proto";

// PetService manages pets.
service PetService {
  rpc GetPet(GetPetRequest) returns (Pet) {
    option (google.api.http) = { get: "/v1/pets/{id}" };
  }
  rpc ListPets(ListPetsRequest) returns (ListPetsResponse);
}
message GetPetRequest { string id = 1; }
message Pet { string id = 1; }
message ListPetsRequest {}
message ListPetsResponse { repeated Pet pets = 1; }
`

const protoNoHTTP = `syntax = "proto3";
package acme.v1;
service Bare {
  rpc Do(Req) returns (Resp);
}
message Req {}
message Resp {}
`

func TestAnalyzeProtoHTTP(t *testing.T) {
	info := analyzeProto("acme/v1/pets.proto", []byte(protoWithHTTP))
	if info.services != 1 {
		t.Errorf("services = %d, want 1", info.services)
	}
	if info.methods != 2 {
		t.Errorf("methods = %d, want 2", info.methods)
	}
	if info.httpMethods != 1 {
		t.Errorf("httpMethods = %d, want 1 (only GetPet is annotated)", info.httpMethods)
	}
}

func TestAnalyzeProtoNoHTTP(t *testing.T) {
	info := analyzeProto("x.proto", []byte(protoNoHTTP))
	if info.methods != 1 || info.httpMethods != 0 {
		t.Errorf("methods=%d http=%d, want 1/0", info.methods, info.httpMethods)
	}
}

func TestBraceBlockNested(t *testing.T) {
	in := "{ option (google.api.http) = { get: \"/x\" }; } trailing"
	body, ok := braceBlock(in)
	if !ok {
		t.Fatal("braceBlock failed to match")
	}
	if !strings.Contains(body, "google.api.http") || strings.Contains(body, "trailing") {
		t.Errorf("braceBlock body wrong: %q", body)
	}
}

func TestResolveProtoRoot(t *testing.T) {
	sep := string(filepath.Separator)
	base := t.TempDir()
	files := []string{
		filepath.Join(base, "proto", "acme", "v1", "common.proto"),
		filepath.Join(base, "proto", "acme", "v1", "pets.proto"),
	}
	// Imports written relative to the "proto" package root.
	imports := []string{"acme/v1/common.proto"}
	got := resolveProtoRoot(files, imports, "fallback")
	want := filepath.Join(base, "proto")
	if got != want {
		t.Errorf("resolveProtoRoot = %q, want %q", got, want)
	}

	// No resolvable imports -> fallback.
	if got := resolveProtoRoot(files, []string{"google/protobuf/any.proto"}, "fb"); got != "fb" {
		t.Errorf("well-known-only imports should fall back, got %q", got)
	}
	_ = sep
}

func TestExecuteProtoNestedImports(t *testing.T) {
	in := t.TempDir()
	writeFile(t, in, "proto/acme/v1/common.proto", "syntax=\"proto3\";\npackage acme.v1;\nmessage Pet { string id = 1; }\n")
	writeFile(t, in, "proto/acme/v1/pets.proto",
		"syntax=\"proto3\";\npackage acme.v1;\nimport \"acme/v1/common.proto\";\nservice S { rpc G(Pet) returns (Pet) { option (google.api.http) = { get: \"/v1/pets\" }; } }\n")
	out := t.TempDir()
	if err := Execute(Options{Inputs: []string{in}, Out: out}); err != nil {
		t.Fatal(err)
	}
	s := firstSource(t, filepath.Join(out, sourcesFileName))
	p, _ := s["proto"].(map[string]any)
	entries, _ := p["entries"].([]any)
	// Entry must be relative to the resolved package root, preserving the import path.
	if len(entries) != 1 || entries[0] != "acme/v1/pets.proto" {
		t.Errorf("entries = %v, want [acme/v1/pets.proto]", p["entries"])
	}
}

func TestCommonDir(t *testing.T) {
	sep := string(filepath.Separator)
	got := commonDir([]string{
		filepath.Join("a", "b", "c"),
		filepath.Join("a", "b", "d"),
	})
	if got != "a"+sep+"b" {
		t.Errorf("commonDir = %q, want a/b", got)
	}
}

func TestExecuteProtoWithHTTP(t *testing.T) {
	in := inputDir(t, "proto/acme/v1/pets.proto", protoWithHTTP)
	out := t.TempDir()
	if err := Execute(Options{Inputs: []string{in}, Out: out}); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	s := firstSource(t, filepath.Join(out, sourcesFileName))
	if s["backend"] != "proto" {
		t.Fatalf("backend = %v", s["backend"])
	}
	p, _ := s["proto"].(map[string]any)
	if p == nil || p["staging"] == nil || p["entries"] == nil {
		t.Errorf("proto block incomplete: %v", s["proto"])
	}

	rep := readReport(t, out)
	src := rep.Sources[0]
	if src.WouldEmitCommands != 1 {
		t.Errorf("wouldEmit = %d, want 1", src.WouldEmitCommands)
	}
	// Compilation cannot be verified offline -> medium, not high.
	if src.Confidence != confMedium {
		t.Errorf("confidence = %q, want medium", src.Confidence)
	}
	if !hasGap(src.Gaps, gapProtoImports, false) {
		t.Errorf("expected advisory proto-imports gap, got %+v", src.Gaps)
	}
}

// A proto tree with no google.api.http RPCs generates nothing and breaks
// bootstrap, so it is not emitted as a source (report-only candidate).
func TestExecuteProtoNoHTTPNotEmitted(t *testing.T) {
	in := inputDir(t, "api.proto", protoNoHTTP)
	err := Execute(Options{Inputs: []string{in}, Out: t.TempDir()})
	var noSrc ErrNoSources
	if err == nil || !asNoSources(err, &noSrc) {
		t.Fatalf("want ErrNoSources (proto without http not emitted), got %v", err)
	}
}
