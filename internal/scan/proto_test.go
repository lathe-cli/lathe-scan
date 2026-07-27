package scan

import (
	"os"
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

func TestResolveProtoClosureExcludesUnreachableFiles(t *testing.T) {
	root := t.TempDir()
	common := filepath.Join(root, "proto", "common.proto")
	service := filepath.Join(root, "proto", "service.proto")
	unused := filepath.Join(root, "proto", "unused.proto")
	files := []string{common, service, unused}
	infos := []protoFileInfo{
		analyzeProto("proto/common.proto", []byte("syntax=\"proto3\"; message Common {}\n")),
		analyzeProto("proto/service.proto", []byte("syntax=\"proto3\";\nimport \"proto/common.proto\";\nservice S {}\n")),
		analyzeProto("proto/unused.proto", []byte("syntax=\"proto3\"; message Unused {}\n")),
	}
	resolution := resolveProtoClosure(files, infos, root)
	if len(resolution.closure) != 2 {
		t.Fatalf("closure = %v, want service and common only", resolution.closure)
	}
	for _, file := range resolution.closure {
		if file == unused {
			t.Fatalf("unreachable file entered closure: %s", file)
		}
	}
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

func TestExecuteProtoMapsGoModuleImportPrefix(t *testing.T) {
	in := t.TempDir()
	writeFile(t, in, "go.mod", "module example.com/demo\n\ngo 1.25\n")
	writeFile(t, in, "api/common.proto", "syntax=\"proto3\"; package api; message Pet { string id = 1; }\n")
	writeFile(t, in, "api/pets.proto",
		"syntax=\"proto3\";\npackage api;\nimport \"example.com/demo/api/common.proto\";\nservice S { rpc G(Pet) returns (Pet) { option (google.api.http) = { get: \"/v1/pets\" }; } }\n")
	out := t.TempDir()
	if err := Execute(Options{Inputs: []string{in}, Out: out}); err != nil {
		t.Fatal(err)
	}
	s := firstSource(t, filepath.Join(out, sourcesFileName))
	p, _ := s["proto"].(map[string]any)
	entries, _ := p["entries"].([]any)
	if len(entries) != 1 || entries[0] != "example.com/demo/api/pets.proto" {
		t.Errorf("entries = %v, want module-prefixed entry", p["entries"])
	}
	roots, _ := p["import_roots"].([]any)
	if len(roots) != 1 || roots[0] != "example.com/demo" {
		t.Errorf("import_roots = %v, want [example.com/demo]", p["import_roots"])
	}
}

// A second Go module in the same tree must not be published under the first
// module's import path: filepath.Join folds the "../sibling" escape into a name
// that resolves to nothing, and `lathe sync-specs` never finds the entry.
func TestExecuteProtoRefusesModulePrefixAcrossModules(t *testing.T) {
	in := t.TempDir()
	writeFile(t, in, "svcA/go.mod", "module example.com/svca\n\ngo 1.25\n")
	writeFile(t, in, "svcA/api/common.proto", "syntax=\"proto3\"; package a; message Pet { string id = 1; }\n")
	writeFile(t, in, "svcA/api/pets.proto",
		"syntax=\"proto3\";\npackage a;\nimport \"example.com/svca/api/common.proto\";\nservice A { rpc G(Pet) returns (Pet) { option (google.api.http) = { get: \"/v1/pets\" }; } }\n")
	writeFile(t, in, "svcB/go.mod", "module example.com/svcb\n\ngo 1.25\n")
	writeFile(t, in, "svcB/api/orders.proto",
		"syntax=\"proto3\";\npackage b;\nmessage Order { string id = 1; }\nservice B { rpc L(Order) returns (Order) { option (google.api.http) = { get: \"/v1/orders\" }; } }\n")
	out := t.TempDir()
	if err := Execute(Options{Inputs: []string{in}, Out: out}); err != nil {
		t.Fatal(err)
	}
	p, _ := firstSource(t, filepath.Join(out, sourcesFileName))["proto"].(map[string]any)
	if p["import_roots"] != nil {
		t.Errorf("import_roots = %v, want none: the closure spans two modules", p["import_roots"])
	}
	entries, _ := p["entries"].([]any)
	if len(entries) != 2 || entries[0] != "svcA/api/pets.proto" || entries[1] != "svcB/api/orders.proto" {
		t.Errorf("entries = %v, want proto-root-relative paths for both modules", entries)
	}
}

// third_party is excluded when choosing the repository's own contract, but the
// googleapis copy living there is still what protoc compiles against; refusing
// to see it would drop a source that builds.
func TestExecuteProtoResolvesVendoredProviderTree(t *testing.T) {
	in := t.TempDir()
	writeFile(t, in, "third_party/googleapis/google/api/annotations.proto",
		"syntax=\"proto3\";\npackage google.api;\nimport \"google/api/http.proto\";\n")
	writeFile(t, in, "third_party/googleapis/google/api/http.proto", "syntax=\"proto3\";\npackage google.api;\n")
	writeFile(t, in, "proto/acme/v1/pets.proto", protoWithHTTP)
	out := t.TempDir()
	if err := Execute(Options{Inputs: []string{in}, Out: out}); err != nil {
		t.Fatal(err)
	}
	srcs := readSources(t, filepath.Join(out, sourcesFileName))
	if len(srcs) != 1 {
		t.Fatalf("want one source, got %v", srcs)
	}
	for name, s := range srcs {
		p, _ := s["proto"].(map[string]any)
		if p["dependencies"] != nil {
			t.Errorf("dependencies = %v, want none: the provider is in the repository", p["dependencies"])
		}
		// Copied under the import path, which is the only name protoc looks for.
		for _, rel := range []string{"google/api/annotations.proto", "google/api/http.proto"} {
			if _, err := os.Stat(filepath.Join(out, name, filepath.FromSlash(rel))); err != nil {
				t.Errorf("provider %s not staged: %v", rel, err)
			}
		}
	}
}

// A pinned source lists paths instead of copying them, so the provider tree
// needs a staging entry of its own; without one `lathe sync-specs` runs protoc
// against a tree that has no google/api/annotations.proto in it.
func TestExecuteProtoStagesVendoredProviderWhenPinned(t *testing.T) {
	in := t.TempDir()
	gitInit(t, in)
	writeFile(t, in, "third_party/googleapis/google/api/annotations.proto",
		"syntax=\"proto3\";\npackage google.api;\n")
	writeFile(t, in, "proto/acme/v1/pets.proto", protoWithHTTP)
	gitInit(t, in, []string{"add", "."}, []string{"commit", "-qm", "protos"},
		[]string{"remote", "add", "origin", "https://example.com/o/r.git"})

	out := t.TempDir()
	if err := Execute(Options{Inputs: []string{in}, Out: out}); err != nil {
		t.Fatal(err)
	}
	s := firstSource(t, filepath.Join(out, sourcesFileName))
	if s["repo_url"] == nil {
		t.Fatalf("clean tracked protos should pin, got %v", s)
	}
	p, _ := s["proto"].(map[string]any)
	staging, _ := p["staging"].([]any)
	got := map[string]any{}
	for _, entry := range staging {
		m, _ := entry.(map[string]any)
		got[m["from"].(string)] = m["to"]
	}
	if len(got) != 2 || got["proto/acme/v1"] != "." || got["third_party/googleapis"] != "." {
		t.Errorf("staging = %v, want the proto root and the provider tree both at .", staging)
	}
}

// A proto import string is scanned source, and it doubles as the name the file
// is staged under. One that walks out of its own root must never become a copy
// destination: nothing may be written outside --out.
func TestExecuteProtoRefusesEscapingImportPath(t *testing.T) {
	base := t.TempDir()
	in, out := filepath.Join(base, "nest", "in"), filepath.Join(base, "nest", "out")
	if err := os.MkdirAll(in, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, in, "testdata/evil/x.proto", "syntax=\"proto3\";\npackage evil;\n")
	writeFile(t, in, "proto/acme/v1/svc.proto",
		"syntax = \"proto3\";\npackage acme.v1;\nimport \"../../../testdata/evil/x.proto\";\nservice S { rpc G(R) returns (R) { option (google.api.http) = { get: \"/v1/x\" }; } }\nmessage R {}\n")
	_ = Execute(Options{Inputs: []string{in}, Out: out})
	if _, err := os.Stat(filepath.Join(base, "testdata")); !os.IsNotExist(err) {
		t.Fatalf("an import path escaped --out and wrote into %s", base)
	}
}

// A digest shape Lathe refuses is not a pin. Writing it would trade a blocking
// gap for a sources.yaml that fails to load at all.
func TestExecuteProtoRejectsMalformedBufPin(t *testing.T) {
	in := inputDir(t, "proto/service.proto", protoWithHTTP)
	writeFile(t, in, "proto/buf.lock", "version: v2\ndeps:\n  - name: buf.build/googleapis/googleapis\n    commit: 004180b77378443887d3b55cabc00384\n    digest: b1:4af5b88c9a1d9b36421ad84a2cff211f\n")
	out := t.TempDir()
	err := Execute(Options{Inputs: []string{in}, Out: out})
	var noSrc ErrNoSources
	if !asNoSources(err, &noSrc) {
		t.Fatalf("v1 digest under a v2 lock was accepted as a pin: %v", err)
	}
	if !hasGap(readReport(t, out).Gaps, gapRefUnresolved, true) {
		t.Fatalf("expected blocking %s gap", gapRefUnresolved)
	}
}

func TestExecuteProtoEmitsPinnedGoModuleDependencies(t *testing.T) {
	in := t.TempDir()
	writeFile(t, in, "go.mod", "module example.com/demo\n\ngo 1.25\n\nrequire (\n  github.com/grpc-ecosystem/grpc-gateway v1.16.0\n  k8s.io/api v0.35.4\n)\n")
	writeFile(t, in, "go.sum", "github.com/grpc-ecosystem/grpc-gateway v1.16.0 h1:gateway\nk8s.io/api v0.35.4 h1:kubernetes\n")
	writeFile(t, in, "api/service.proto",
		"syntax=\"proto3\";\npackage api;\nimport \"google/api/annotations.proto\";\nimport \"k8s.io/api/core/v1/generated.proto\";\nservice S { rpc G(Request) returns (Request) { option (google.api.http) = { get: \"/v1/demo\" }; } }\nmessage Request {}\n")
	out := t.TempDir()
	if err := Execute(Options{Inputs: []string{in}, Out: out}); err != nil {
		t.Fatal(err)
	}
	s := firstSource(t, filepath.Join(out, sourcesFileName))
	p, _ := s["proto"].(map[string]any)
	deps, _ := p["dependencies"].([]any)
	if len(deps) != 2 {
		t.Fatalf("dependencies = %v, want grpc-gateway and k8s.io/api", p["dependencies"])
	}
	first, _ := deps[0].(map[string]any)
	second, _ := deps[1].(map[string]any)
	if first["module"] != "github.com/grpc-ecosystem/grpc-gateway" || second["module"] != "k8s.io/api" {
		t.Errorf("dependencies = %v", deps)
	}
}

func TestExecuteProtoRejectsUnresolvedGoogleAPIImport(t *testing.T) {
	in := t.TempDir()
	writeFile(t, in, "api/service.proto",
		"syntax=\"proto3\";\npackage api;\nimport \"google/api/annotations.proto\";\nservice S { rpc G(Request) returns (Request) { option (google.api.http) = { get: \"/v1/demo\" }; } }\nmessage Request {}\n")
	out := t.TempDir()
	err := Execute(Options{Inputs: []string{in}, Out: out})
	var noSrc ErrNoSources
	if !asNoSources(err, &noSrc) {
		t.Fatalf("want ErrNoSources, got %v", err)
	}
	if !hasGap(readReport(t, out).Gaps, gapRefUnresolved, true) {
		t.Fatalf("expected blocking %s gap", gapRefUnresolved)
	}
}

func TestExecuteProtoDoesNotReadBufLockOutsideInput(t *testing.T) {
	outside := filepath.Join(t.TempDir(), "buf.lock")
	if err := os.WriteFile(outside, []byte("version: v2\ndeps:\n  - name: buf.build/googleapis/googleapis\n    commit: 004180b77378443887d3b55cabc00384\n    digest: b5:e8f475fe3330f31f5fd86ac689093bcd274e19611a09db91f41d637cb9197881ce89882b94d13a58738e53c91c6e4bae7dc1feba85f590164c975a89e25115dc\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	in := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(in, "buf.lock")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, in, "service.proto", protoWithHTTP)
	out := t.TempDir()
	err := Execute(Options{Inputs: []string{in}, Out: out})
	var noSrc ErrNoSources
	if !asNoSources(err, &noSrc) {
		t.Fatalf("outside buf.lock supplied a dependency: %v", err)
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
	writeFile(t, in, "proto/buf.lock", "version: v2\ndeps:\n  - name: buf.build/googleapis/googleapis\n    commit: 004180b77378443887d3b55cabc00384\n    digest: b5:e8f475fe3330f31f5fd86ac689093bcd274e19611a09db91f41d637cb9197881ce89882b94d13a58738e53c91c6e4bae7dc1feba85f590164c975a89e25115dc\n")
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
	deps, _ := p["dependencies"].([]any)
	if len(deps) != 1 {
		t.Fatalf("dependencies = %v, want one Buf dependency", p["dependencies"])
	}
	dep, _ := deps[0].(map[string]any)
	if dep["kind"] != protoDependencyBuf || dep["module"] != "buf.build/googleapis/googleapis" || dep["lock_version"] != "v2" {
		t.Errorf("dependency = %v, want pinned googleapis Buf module", dep)
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

func TestExecuteProtoReadsBufV1Lock(t *testing.T) {
	in := inputDir(t, "proto/service.proto", protoWithHTTP)
	writeFile(t, in, "proto/buf.lock", "version: v1\ndeps:\n  - remote: buf.build\n    owner: googleapis\n    repository: googleapis\n    commit: 004180b77378443887d3b55cabc00384\n    digest: b4:4af5b88c9a1d9b36421ad84a2cff211fc74995040188dafc1c8508d36406140e40eb0ab82d21e761961e4a71631d4474e3d0608b987ca3d02d5d19012edff21d\n")
	out := t.TempDir()
	if err := Execute(Options{Inputs: []string{in}, Out: out}); err != nil {
		t.Fatal(err)
	}
	s := firstSource(t, filepath.Join(out, sourcesFileName))
	p, _ := s["proto"].(map[string]any)
	deps, _ := p["dependencies"].([]any)
	if len(deps) != 1 {
		t.Fatalf("dependencies = %v", p["dependencies"])
	}
	dep, _ := deps[0].(map[string]any)
	if dep["module"] != "buf.build/googleapis/googleapis" || dep["lock_version"] != "v1" {
		t.Fatalf("v1 dependency = %v", dep)
	}
}

// A proto tree with no google.api.http RPCs generates nothing and breaks
// bootstrap, so it is not emitted as a source (report-only candidate).
func TestExecuteProtoNoHTTPNotEmitted(t *testing.T) {
	in := inputDir(t, "api.proto", protoNoHTTP)
	out := t.TempDir()
	err := Execute(Options{Inputs: []string{in}, Out: out})
	var noSrc ErrNoSources
	if err == nil || !asNoSources(err, &noSrc) {
		t.Fatalf("want ErrNoSources (proto without http not emitted), got %v", err)
	}
	if !hasGap(readReport(t, out).Gaps, gapProtoNoHTTP, true) {
		t.Errorf("expected blocking %s gap, got %+v", gapProtoNoHTTP, readReport(t, out).Gaps)
	}
}
