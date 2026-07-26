package scan

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// One operation each, so --prefer decides the tie rather than command count.
const specOneOp = `openapi: 3.0.3
info: { title: Tie, version: "1" }
paths:
  /a:
    get:
      operationId: a
      responses:
        "200": { description: ok }
`

const sdlOneQuery = `type Query { a: String }
`

func readSources(t *testing.T, path string) map[string]map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var f struct {
		Sources map[string]map[string]any `yaml:"sources"`
	}
	if err := yaml.Unmarshal(data, &f); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return f.Sources
}

func TestNormalizeInputsCollapsesFilesystemAliases(t *testing.T) {
	root := t.TempDir()
	alias := filepath.Join(t.TempDir(), "repo")
	if err := os.Symlink(root, alias); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	inputs, keys := normalizeInputs([]string{alias, root})
	if len(inputs) != 1 || len(keys) != 1 {
		t.Fatalf("normalizeInputs returned %d inputs and %d keys, want one of each", len(inputs), len(keys))
	}
	physical, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	if inputs[0].key != physical {
		t.Fatalf("normalized key = %q, want %q", inputs[0].key, physical)
	}
}

func inputDir(t *testing.T, rel, spec string) string {
	t.Helper()
	d := t.TempDir()
	writeFile(t, d, rel, spec)
	return d
}

func TestExecuteLocalSource(t *testing.T) {
	in := inputDir(t, "openapi.yaml", specOpenAPI)
	out := t.TempDir()
	if err := Execute(Options{Inputs: []string{in}, Out: out}); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	srcs := readSources(t, filepath.Join(out, sourcesFileName))
	if len(srcs) != 1 {
		t.Fatalf("want 1 source, got %d: %v", len(srcs), srcs)
	}
	s := srcs["billing_api"]
	if s == nil {
		t.Fatalf("source billing-api missing, got %v", srcs)
	}
	if s["backend"] != "openapi3" {
		t.Errorf("backend = %v", s["backend"])
	}
	if s["local_path"] != "billing_api" {
		t.Errorf("local_path = %v, want billing-api", s["local_path"])
	}
	if s["repo_url"] != nil {
		t.Errorf("local source must not emit repo_url, got %v", s["repo_url"])
	}
	if s["default_hostname"] != "api.acme.com" {
		t.Errorf("default_hostname = %v", s["default_hostname"])
	}
	if _, err := os.Stat(filepath.Join(out, "billing_api", "openapi.yaml")); err != nil {
		t.Errorf("copied spec missing: %v", err)
	}
	for _, f := range []string{reportFileName, gapsFileName} {
		if _, err := os.Stat(filepath.Join(out, f)); err != nil {
			t.Errorf("%s missing: %v", f, err)
		}
	}
}

func TestExecuteEmptyIsNoSources(t *testing.T) {
	err := Execute(Options{Inputs: []string{t.TempDir()}, Out: t.TempDir()})
	var noSrc ErrNoSources
	if !errors.As(err, &noSrc) {
		t.Fatalf("want ErrNoSources, got %v", err)
	}
}

// A spec Lathe would generate nothing from is not a source. It must not reach
// sources.yaml, the run must exit 2 — and the audit must still explain why.
func TestExecuteZeroOperationsBlocks(t *testing.T) {
	spec := "openapi: 3.0.0\ninfo:\n  title: Empty API\npaths: {}\n"
	in := inputDir(t, "openapi.yaml", spec)
	out := t.TempDir()

	err := Execute(Options{Inputs: []string{in}, Out: out})
	var noSrc ErrNoSources
	if !errors.As(err, &noSrc) {
		t.Fatalf("want ErrNoSources, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(out, sourcesFileName)); !os.IsNotExist(err) {
		t.Errorf("sources.yaml must not be written when nothing is usable (%v)", err)
	}

	rep := readReport(t, out)
	if len(rep.Sources) != 0 {
		t.Errorf("blocked source must not be reported as a source: %+v", rep.Sources)
	}
	if rep.Summary.ExitCode != 2 {
		t.Errorf("summary.exit_code = %d, want 2", rep.Summary.ExitCode)
	}
	if !hasGap(rep.Gaps, gapParseError, true) {
		t.Errorf("expected blocking %s gap at report top level, got %+v", gapParseError, rep.Gaps)
	}
	gaps, err := os.ReadFile(filepath.Join(out, gapsFileName))
	if err != nil {
		t.Fatalf("GAPS.md missing: %v", err)
	}
	if !strings.Contains(string(gaps), gapParseError) {
		t.Errorf("GAPS.md does not render the blocking gap:\n%s", gaps)
	}
}

// summary.usable must mirror the process exit code, or scripts cannot trust it.
func TestExecuteUsableMatchesExitCode(t *testing.T) {
	in := inputDir(t, "openapi.yaml", specOpenAPI)
	out := t.TempDir()
	if err := Execute(Options{Inputs: []string{in}, Out: out}); err != nil {
		t.Fatal(err)
	}
	rep := readReport(t, out)
	if rep.Summary.Usable != 1 || rep.Summary.ExitCode != 0 {
		t.Errorf("usable = %d, exit_code = %d, want 1 and 0", rep.Summary.Usable, rep.Summary.ExitCode)
	}
}

func TestExecuteHostnameGapWhenNoServer(t *testing.T) {
	spec := "openapi: 3.0.0\ninfo:\n  title: No Host\npaths:\n  /x:\n    get:\n      responses:\n        \"200\": {description: ok}\n"
	in := inputDir(t, "openapi.yaml", spec)
	out := t.TempDir()
	if err := Execute(Options{Inputs: []string{in}, Out: out}); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	rep := readReport(t, out)
	s := rep.Sources[0]
	if s.Confidence != confHigh {
		t.Errorf("confidence = %q, want high (hostname gap is advisory)", s.Confidence)
	}
	if !hasGap(s.Gaps, gapAmbiguousHost, false) {
		t.Errorf("expected advisory %s gap, got %+v", gapAmbiguousHost, s.Gaps)
	}
}

func TestExecuteDeterministic(t *testing.T) {
	in1 := inputDir(t, "openapi.yaml", specOpenAPI)
	in2 := inputDir(t, "swagger.json", specSwagger)
	run := func() ([]byte, []byte) {
		out := t.TempDir()
		if err := Execute(Options{Inputs: []string{in1, in2}, Out: out}); err != nil {
			t.Fatal(err)
		}
		sy, _ := os.ReadFile(filepath.Join(out, sourcesFileName))
		rj, _ := os.ReadFile(filepath.Join(out, reportFileName))
		return sy, rj
	}
	sy1, rj1 := run()
	sy2, rj2 := run()
	if string(sy1) != string(sy2) {
		t.Errorf("sources.yaml not deterministic:\n%s\n---\n%s", sy1, sy2)
	}
	if string(rj1) != string(rj2) {
		t.Errorf("report.json not deterministic")
	}
}

func TestExecuteMultiInputAggregates(t *testing.T) {
	in1 := inputDir(t, "openapi.yaml", specOpenAPI)
	in2 := inputDir(t, "swagger.json", specSwagger)
	out := t.TempDir()
	if err := Execute(Options{Inputs: []string{in1, in2}, Out: out}); err != nil {
		t.Fatal(err)
	}
	srcs := readSources(t, filepath.Join(out, sourcesFileName))
	if len(srcs) != 2 {
		t.Fatalf("want 2 aggregated sources, got %d: %v", len(srcs), srcs)
	}
}

func TestExecuteNameRejectsMultiple(t *testing.T) {
	in1 := inputDir(t, "openapi.yaml", specOpenAPI)
	in2 := inputDir(t, "swagger.json", specSwagger)
	err := Execute(Options{Inputs: []string{in1, in2}, Out: t.TempDir(), Name: "x"})
	if err == nil {
		t.Fatal("expected error: --name with multiple sources")
	}
}

func TestExecuteNameSingle(t *testing.T) {
	in := inputDir(t, "openapi.yaml", specOpenAPI)
	out := t.TempDir()
	if err := Execute(Options{Inputs: []string{in}, Out: out, Name: "custom"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := readSources(t, filepath.Join(out, sourcesFileName))["custom"]; !ok {
		t.Error("--name did not override source name")
	}
}

func TestExecuteRefusesNonEmptyOut(t *testing.T) {
	in := inputDir(t, "openapi.yaml", specOpenAPI)
	out := t.TempDir()
	writeFile(t, out, "existing.txt", "keep me")

	err := Execute(Options{Inputs: []string{in}, Out: out})
	if err == nil {
		t.Fatal("expected refusal for non-empty --out")
	}
	if err := Execute(Options{Inputs: []string{in}, Out: out, Force: true}); err != nil {
		t.Fatalf("--force should allow non-empty out: %v", err)
	}
}

func TestExecuteMergePreservesForeign(t *testing.T) {
	out := t.TempDir()
	// Foreign backend this run does not emit; --merge must preserve it.
	foreign := `sources:
  legacy-graph:
    repo_url: https://github.com/acme/g.git
    pinned_tag: v1.0.0
    backend: graphql
    graphql:
      schema: schema.graphql
      expose:
        queries: [listApps]
`
	writeFile(t, out, sourcesFileName, foreign)

	in := inputDir(t, "openapi.yaml", specOpenAPI)
	if err := Execute(Options{Inputs: []string{in}, Out: out, Merge: true}); err != nil {
		t.Fatalf("Execute merge: %v", err)
	}

	srcs := readSources(t, filepath.Join(out, sourcesFileName))
	if _, ok := srcs["legacy-graph"]; !ok {
		t.Error("--merge dropped the foreign graphql source")
	}
	if _, ok := srcs["billing_api"]; !ok {
		t.Error("--merge did not add the new source")
	}
	g, _ := srcs["legacy-graph"]["graphql"].(map[string]any)
	if g == nil || g["schema"] != "schema.graphql" {
		t.Errorf("foreign graphql block corrupted: %v", srcs["legacy-graph"])
	}
}

// Re-scanning the same input with --merge must update its entry, not append a
// copy: a non-idempotent merge grows sources.yaml without bound.
func TestExecuteMergeIsIdempotent(t *testing.T) {
	in := inputDir(t, "openapi.yaml", specOpenAPI)
	out := t.TempDir()

	var first []byte
	for i := 1; i <= 3; i++ {
		if err := Execute(Options{Inputs: []string{in}, Out: out, Merge: true}); err != nil {
			t.Fatalf("merge run %d: %v", i, err)
		}
		srcs := readSources(t, filepath.Join(out, sourcesFileName))
		if len(srcs) != 1 {
			t.Fatalf("run %d: want 1 source, got %d: %v", i, len(srcs), srcs)
		}
		if _, ok := srcs["billing_api"]; !ok {
			t.Fatalf("run %d: source renamed: %v", i, srcs)
		}
		data, err := os.ReadFile(filepath.Join(out, sourcesFileName))
		if err != nil {
			t.Fatal(err)
		}
		if i == 1 {
			first = data
		} else if string(data) != string(first) {
			t.Fatalf("run %d changed sources.yaml:\n%s\n---\n%s", i, first, data)
		}
	}
}

// The workflow --merge exists for: a second module, scanned later, joins the
// manifest without disturbing what is already there.
func TestExecuteMergeAddsNewInput(t *testing.T) {
	billing := inputDir(t, "openapi.yaml", specOpenAPI)
	inventory := inputDir(t, "swagger.json", specSwagger)
	out := t.TempDir()

	if err := Execute(Options{Inputs: []string{billing}, Out: out, Merge: true}); err != nil {
		t.Fatal(err)
	}
	if err := Execute(Options{Inputs: []string{inventory}, Out: out, Merge: true}); err != nil {
		t.Fatal(err)
	}
	srcs := readSources(t, filepath.Join(out, sourcesFileName))
	if len(srcs) != 2 {
		t.Fatalf("want both modules, got %d: %v", len(srcs), srcs)
	}
	if _, ok := srcs["billing_api"]; !ok {
		t.Errorf("--merge dropped the earlier module: %v", srcs)
	}
	// The first module's copied material must survive the second run untouched.
	if _, err := os.Stat(filepath.Join(out, "billing_api", "openapi.yaml")); err != nil {
		t.Errorf("earlier module's copied spec was removed: %v", err)
	}
}

// A --merge whose input produced nothing learned nothing about that API: a
// mistyped path must not delete a working entry plus the policy written onto it.
func TestExecuteMergeKeepsEntryWhenInputProducesNothing(t *testing.T) {
	billing := inputDir(t, "api/openapi.yaml", specOpenAPI)
	inventory := inputDir(t, "api/swagger.json", specSwagger)
	out := t.TempDir()
	if err := Execute(Options{Inputs: []string{billing, inventory}, Out: out, Merge: true}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(out, sourcesFileName)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	edited := strings.Replace(string(raw), "    billing_api:\n",
		"    billing_api:\n        display_name: Billing\n        groups: [finance]\n", 1)
	if edited == string(raw) {
		t.Fatalf("could not attach policy to billing_api:\n%s", raw)
	}
	if err := os.WriteFile(path, []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}

	// The spec is still there but no longer yields a source: an unresolvable
	// external $ref is blocking, so the input produces nothing this run.
	writeFile(t, billing, "api/openapi.yaml", specOpenAPI+
		"    parameters:\n      - $ref: \"./gone.yaml#/components/parameters/P\"\n")
	err = Execute(Options{Inputs: []string{billing}, Out: out, Merge: true})
	var noSrc ErrNoSources
	if !errors.As(err, &noSrc) {
		t.Fatalf("want ErrNoSources, got %v", err)
	}

	srcs := readSources(t, path)
	s, ok := srcs["billing_api"]
	if !ok {
		t.Fatalf("--merge deleted the entry for an input it produced nothing from: %v", srcs)
	}
	if s["display_name"] != "Billing" || s["groups"] == nil {
		t.Errorf("hand-written policy was lost: %v", s)
	}
	if _, ok := srcs["legacy_api"]; !ok {
		t.Errorf("an unrelated input's entry was dropped: %v", srcs)
	}
	rep := readReport(t, out)
	if !hasGap(rep.Gaps, gapSourceKept, false) {
		t.Errorf("keeping the entry was not reported as a %s gap: %+v", gapSourceKept, rep.Gaps)
	}
	// Ownership must survive, or the next --merge treats the entry as foreign.
	var owned bool
	for _, p := range rep.Preserved {
		if p.Name == "billing_api" && p.Provenance != nil {
			owned = true
		}
	}
	if !owned {
		t.Errorf("kept entry lost its provenance: %+v", rep.Preserved)
	}
}

// The whole manifest is the same case: a --merge that produced nothing must not
// take the last entry — and with it the file — down with it.
func TestExecuteMergeKeepsSoleEntryWhenInputVanishes(t *testing.T) {
	in := inputDir(t, "api/openapi.yaml", specOpenAPI)
	out := t.TempDir()
	if err := Execute(Options{Inputs: []string{in}, Out: out, Merge: true}); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(in); err != nil {
		t.Fatal(err)
	}
	err := Execute(Options{Inputs: []string{in}, Out: out, Merge: true})
	var noSrc ErrNoSources
	if !errors.As(err, &noSrc) {
		t.Fatalf("want ErrNoSources, got %v", err)
	}
	srcs := readSources(t, filepath.Join(out, sourcesFileName))
	if _, ok := srcs["billing_api"]; !ok {
		t.Errorf("an unreadable input took the whole manifest with it: %v", srcs)
	}
}

// A pre-provenance report carries no recoverable ownership; --merge must refuse
// rather than treat every entry as foreign and append suffixed duplicates.
func TestExecuteMergeRefusesReportWithoutProvenance(t *testing.T) {
	in := inputDir(t, "api/openapi.yaml", specOpenAPI)
	out := t.TempDir()
	if err := Execute(Options{Inputs: []string{in}, Out: out, Merge: true}); err != nil {
		t.Fatal(err)
	}
	sourcesPath := filepath.Join(out, sourcesFileName)
	before, err := os.ReadFile(sourcesPath)
	if err != nil {
		t.Fatal(err)
	}

	// Strip provenance the way a pre-provenance release would have left it.
	reportPath := filepath.Join(out, reportFileName)
	var raw map[string]any
	data, err := os.ReadFile(reportPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	srcs, _ := raw["sources"].([]any)
	if len(srcs) == 0 {
		t.Fatalf("seed run recorded no sources: %s", data)
	}
	for _, s := range srcs {
		delete(s.(map[string]any), "provenance")
		delete(s.(map[string]any), "input")
	}
	legacy, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(reportPath, legacy, 0o644); err != nil {
		t.Fatal(err)
	}

	err = Execute(Options{Inputs: []string{in}, Out: out, Merge: true})
	if err == nil {
		t.Fatal("--merge accepted a report with no provenance")
	}
	var noSrc ErrNoSources
	var write ErrWrite
	if errors.As(err, &noSrc) || errors.As(err, &write) {
		t.Errorf("legacy --merge should fail as usage, got %T: %v", err, err)
	}
	if !strings.Contains(err.Error(), "provenance") {
		t.Errorf("error does not say why the merge was refused: %v", err)
	}
	// Refusing must happen before anything is written.
	after, err := os.ReadFile(sourcesPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Errorf("a refused --merge rewrote the manifest:\n%s", after)
	}
	for name := range readSources(t, sourcesPath) {
		if strings.HasSuffix(name, "_2") {
			t.Errorf("a duplicate %q was appended despite the refusal", name)
		}
	}
}

// Merging into a manifest this tool never wrote is the legitimate case that must
// keep working: no report.json at all means every entry is foreign, not legacy.
func TestExecuteMergeIntoHandWrittenManifestStillWorks(t *testing.T) {
	in := inputDir(t, "api/openapi.yaml", specOpenAPI)
	out := t.TempDir()
	writeFile(t, out, sourcesFileName, "sources:\n    handwritten:\n        backend: openapi3\n"+
		"        local_path: elsewhere\n        openapi3:\n            files:\n                - spec.yaml\n")
	if err := Execute(Options{Inputs: []string{in}, Out: out, Merge: true}); err != nil {
		t.Fatalf("--merge into a foreign manifest was refused: %v", err)
	}
	srcs := readSources(t, filepath.Join(out, sourcesFileName))
	if _, ok := srcs["handwritten"]; !ok {
		t.Errorf("foreign entry was dropped: %v", srcs)
	}
	if len(srcs) != 2 {
		t.Errorf("want the foreign entry plus the scanned one, got %v", srcs)
	}
}

// Provenance must not move when the corpus grows: a key derived from the files
// found (an inferred proto root) re-points the entry and drops its policy.
func TestExecuteProtoIdentitySurvivesNewPackage(t *testing.T) {
	proto := func(pkg, path string) string {
		return "syntax = \"proto3\";\npackage " + pkg + ";\nimport \"google/api/annotations.proto\";\n" +
			"service Svc" + pkg + " {\n  rpc Get(Req) returns (Resp) { option (google.api.http) = { get: \"" + path + "\" }; }\n}\n" +
			"message Req {}\nmessage Resp {}\n"
	}
	in := t.TempDir()
	writeFile(t, in, "api/v1/foo.proto", proto("v1", "/v1/foo"))
	out := t.TempDir()
	if err := Execute(Options{Inputs: []string{in}, Out: out, Merge: true}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(out, sourcesFileName)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	name := ""
	for n := range readSources(t, path) {
		name = n
	}
	if err := os.WriteFile(path, []byte(strings.Replace(string(raw), "        backend: proto\n",
		"        backend: proto\n        display_name: Platform\n        groups: [core]\n", 1)), 0o644); err != nil {
		t.Fatal(err)
	}

	// A second package moves the inferred proto root from api/v1 up to api.
	writeFile(t, in, "api/v2/bar.proto", proto("v2", "/v2/bar"))
	if err := Execute(Options{Inputs: []string{in}, Out: out, Merge: true}); err != nil {
		t.Fatal(err)
	}
	srcs := readSources(t, path)
	if len(srcs) != 1 {
		t.Fatalf("adding a package duplicated the source: %v", srcs)
	}
	s, ok := srcs[name]
	if !ok {
		t.Fatalf("entry %q was re-pointed when the proto root moved: %v", name, srcs)
	}
	if s["display_name"] != "Platform" || s["groups"] == nil {
		t.Errorf("hand-written policy was dropped when the proto root moved: %v", s)
	}
}

// Fail-closed must hold for unreadable policy too: otherwise a typo in expose
// is enough to publish the whole discovered surface.
func TestExecuteMergeUnreadableExposeFailsClosed(t *testing.T) {
	in := inputDir(t, "schema.graphql", "type Query { a: String b: String }\n")
	out := t.TempDir()
	if err := Execute(Options{Inputs: []string{in}, Out: out, Merge: true}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(out, sourcesFileName)
	name := ""
	for n := range readSources(t, path) {
		name = n
	}
	if err := os.WriteFile(path, []byte("sources:\n    "+name+":\n        local_path: "+name+
		"\n        backend: graphql\n        graphql:\n            schema: schema.graphql\n"+
		"            expose:\n                queries: 5\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := Execute(Options{Inputs: []string{in}, Out: out, Merge: true})
	var noSrc ErrNoSources
	if !errors.As(err, &noSrc) {
		t.Fatalf("want ErrNoSources, got %v", err)
	}
	s := firstSource(t, path)
	gql, _ := s["graphql"].(map[string]any)
	exp, _ := gql["expose"].(map[string]any)
	if exp["queries"] != 5 {
		t.Errorf("unreadable expose was replaced with the discovered surface: %v", exp)
	}
	if !hasGap(readReport(t, out).Gaps, gapExposeUnreadable, true) {
		t.Errorf("no blocking %s gap was raised", gapExposeUnreadable)
	}
}

// An empty run has to say why. A spec that would not parse is the usual reason,
// and reporting it only inside candidates[] leaves GAPS.md with nothing to show.
func TestExecuteMalformedSpecIsABlockingGap(t *testing.T) {
	in := inputDir(t, "api/openapi.yaml", "openapi: \"3.0.0\"\ninfo: {title: x\n  bad: [unclosed\n")
	out := t.TempDir()
	err := Execute(Options{Inputs: []string{in}, Out: out})
	var noSrc ErrNoSources
	if !errors.As(err, &noSrc) {
		t.Fatalf("want ErrNoSources, got %v", err)
	}
	if !hasGap(readReport(t, out).Gaps, gapParseError, true) {
		t.Errorf("malformed spec produced no blocking %s gap", gapParseError)
	}
	gaps, rerr := os.ReadFile(filepath.Join(out, gapsFileName))
	if rerr != nil {
		t.Fatal(rerr)
	}
	if !strings.Contains(string(gaps), "## Blocking") {
		t.Errorf("GAPS.md reports an empty run with no blocking section:\n%s", gaps)
	}
}

// Discovery has to honor the rules a parent declares: scanning repo/sub must not
// select what `git check-ignore` inside repo already excludes.
func TestIndexFilesHonorsParentGitignore(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, ".gitignore", "sub/private/\n")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, repo, "sub/private/openapi.yaml", specOpenAPI)
	writeFile(t, repo, "sub/api/openapi.yaml", specOpenAPI)

	got := indexFiles(filepath.Join(repo, "sub")).specs
	if len(got) != 1 || filepath.Base(filepath.Dir(got[0])) != "api" {
		t.Errorf("parent .gitignore was not applied to a subdirectory scan: %v", got)
	}
}

// .gitignore never applies across a repository boundary: a checkout nested
// inside another repo must not be blanked by the outer repo's rules.
func TestIndexFilesStopsAtRepositoryBoundary(t *testing.T) {
	outer := t.TempDir()
	writeFile(t, outer, ".gitignore", "/work/\n")
	for _, dir := range []string{".git", "work/inner/.git"} {
		if err := os.MkdirAll(filepath.Join(outer, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// In a subdirectory: a leaked outer dir-only rule kills it via SkipDir. A
	// spec at the walk root would survive even without the boundary guard (the
	// root is exempt from the ignore check) and prove nothing.
	writeFile(t, outer, "work/inner/api/openapi.yaml", specOpenAPI)

	if got := indexFiles(filepath.Join(outer, "work", "inner")).specs; len(got) != 1 {
		t.Errorf("outer repo's .gitignore leaked into a nested repository: %v", got)
	}
}

// No scan may derive deletions from report.json — it is an ordinary editable
// file. A rescan rewrites the manifest and leaves everything else alone.
func TestExecuteNeverDeletesFromOutDir(t *testing.T) {
	alpha := inputDir(t, "api/openapi.yaml", specOpenAPI)
	beta := inputDir(t, "api/swagger.json", specSwagger)
	out := t.TempDir()

	if err := Execute(Options{Inputs: []string{alpha}, Out: out}); err != nil {
		t.Fatal(err)
	}
	writeFile(t, out, "my-notes.txt", "precious")

	// A report claiming the source directory is "." — filepath.IsLocal(".") is
	// true, so any path-based cleanup here would take out the whole tree.
	rep := readReport(t, out)
	rep.Sources[0].Origin.LocalPath = "."
	data, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(out, reportFileName), data, 0o644); err != nil {
		t.Fatal(err)
	}

	if err := Execute(Options{Inputs: []string{beta}, Out: out, Force: true}); err != nil {
		t.Fatal(err)
	}
	for _, keep := range []string{"my-notes.txt", sourcesFileName, reportFileName, gapsFileName, "billing_api"} {
		if _, err := os.Stat(filepath.Join(out, keep)); err != nil {
			t.Errorf("%s was deleted from --out: %v", keep, err)
		}
	}
	// The manifest is still rewritten to describe only this run.
	srcs := readSources(t, filepath.Join(out, sourcesFileName))
	if _, gone := srcs["billing_api"]; gone || len(srcs) != 1 {
		t.Errorf("manifest should describe only the new scan, got %v", srcs)
	}
}

// A run that produces nothing must not leave an earlier manifest claiming
// otherwise; the next reader would trust sources this scan just contradicted.
func TestExecuteZeroResultRemovesStaleManifest(t *testing.T) {
	in := inputDir(t, "api/openapi.yaml", specOpenAPI)
	out := t.TempDir()
	if err := Execute(Options{Inputs: []string{in}, Out: out}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(out, sourcesFileName)); err != nil {
		t.Fatalf("first run wrote no manifest: %v", err)
	}

	err := Execute(Options{Inputs: []string{t.TempDir()}, Out: out, Force: true})
	var noSrc ErrNoSources
	if !errors.As(err, &noSrc) {
		t.Fatalf("want ErrNoSources, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(out, sourcesFileName)); !os.IsNotExist(err) {
		t.Errorf("stale sources.yaml survived a zero-result run (%v)", err)
	}
}

// graphql.expose is human policy: scan lists everything and asks the user to
// trim it, so a re-merge must not silently restore the surface they removed.
func TestExecuteMergePreservesTrimmedExpose(t *testing.T) {
	in := inputDir(t, "schema.graphql", sdlConsole)
	out := t.TempDir()
	if err := Execute(Options{Inputs: []string{in}, Out: out, Merge: true}); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(out, sourcesFileName)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var sf struct {
		Sources map[string]map[string]any `yaml:"sources"`
	}
	if err := yaml.Unmarshal(raw, &sf); err != nil {
		t.Fatal(err)
	}
	for name := range sf.Sources {
		g, _ := sf.Sources[name]["graphql"].(map[string]any)
		g["expose"] = map[string]any{"queries": []any{"listApps"}}
	}
	trimmed, err := yaml.Marshal(sf)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, trimmed, 0o644); err != nil {
		t.Fatal(err)
	}

	if err := Execute(Options{Inputs: []string{in}, Out: out, Merge: true}); err != nil {
		t.Fatal(err)
	}
	s := firstSource(t, path)
	g, _ := s["graphql"].(map[string]any)
	exp, _ := g["expose"].(map[string]any)
	queries, _ := exp["queries"].([]any)
	if len(queries) != 1 || queries[0] != "listApps" {
		t.Errorf("--merge restored the trimmed expose: %v", exp)
	}
	if exp["mutations"] != nil {
		t.Errorf("--merge re-added mutations the user removed: %v", exp)
	}
	if !hasGap(readReport(t, out).Sources[0].Gaps, gapExposePreserved, false) {
		t.Error("keeping a trimmed expose must be reported, not silent")
	}
}

// report.json has to account for every entry in the manifest it sits next to.
func TestExecuteReportAccountsForWholeManifest(t *testing.T) {
	out := t.TempDir()
	writeFile(t, out, sourcesFileName, `sources:
  legacy-graph:
    repo_url: https://github.com/acme/g.git
    pinned_tag: v1.0.0
    backend: graphql
    graphql:
      schema: schema.graphql
      expose:
        queries: [listApps]
`)
	in := inputDir(t, "openapi.yaml", specOpenAPI)
	if err := Execute(Options{Inputs: []string{in}, Out: out, Merge: true}); err != nil {
		t.Fatal(err)
	}

	srcs := readSources(t, filepath.Join(out, sourcesFileName))
	rep := readReport(t, out)
	if len(rep.Sources)+len(rep.Preserved) != len(srcs) {
		t.Errorf("sources(%d) + preserved(%d) != manifest(%d)", len(rep.Sources), len(rep.Preserved), len(srcs))
	}
	if len(rep.Preserved) != 1 || rep.Preserved[0].Name != "legacy-graph" {
		t.Errorf("preserved = %v, want [legacy-graph]", rep.Preserved)
	}
	if rep.Preserved[0].Provenance != nil {
		t.Errorf("a hand-written foreign entry has no provenance, got %+v", rep.Preserved[0].Provenance)
	}
}

// Scanning A, then B, then A again: ownership must survive rounds where a
// source is only carried, so the third run still recognizes A's entry as ours.
func TestExecuteMergeOwnershipSurvivesRoundTrip(t *testing.T) {
	a := inputDir(t, "api/openapi.yaml", specOpenAPI)
	b := inputDir(t, "api/swagger.json", specSwagger)
	out := t.TempDir()

	names := func() []string {
		t.Helper()
		var got []string
		for n := range readSources(t, filepath.Join(out, sourcesFileName)) {
			got = append(got, n)
		}
		sort.Strings(got)
		return got
	}

	for _, in := range []string{a, b, a} {
		if err := Execute(Options{Inputs: []string{in}, Out: out, Merge: true}); err != nil {
			t.Fatalf("merge %s: %v", in, err)
		}
	}
	got := names()
	if len(got) != 2 {
		t.Fatalf("A -> B -> A produced %d entries (%v); re-scanning A must update its own entry", len(got), got)
	}
	for _, n := range got {
		if strings.HasSuffix(n, "_2") {
			t.Errorf("A -> B -> A created a suffixed duplicate: %v", got)
		}
	}
}

// A trimmed expose with no intersection left means every remembered decision is
// stale; writing the new surface would publish operations nobody approved.
func TestExecuteMergeStaleExposeFailsClosed(t *testing.T) {
	in := t.TempDir()
	writeFile(t, in, "schema.graphql", "type Query { oldQuery: String }\n")
	out := t.TempDir()
	if err := Execute(Options{Inputs: []string{in}, Out: out, Merge: true}); err != nil {
		t.Fatal(err)
	}

	writeFile(t, in, "schema.graphql", "type Query { newQuery: String\n  secretAdmin: String }\n")
	err := Execute(Options{Inputs: []string{in}, Out: out, Merge: true})
	// Nothing was written, so the run reports exactly that rather than implying
	// the manifest was refreshed.
	var noSrc ErrNoSources
	if !errors.As(err, &noSrc) {
		t.Fatalf("declining to update leaves nothing written; want ErrNoSources, got %v", err)
	}

	s := firstSource(t, filepath.Join(out, sourcesFileName))
	g, _ := s["graphql"].(map[string]any)
	exp, _ := g["expose"].(map[string]any)
	queries, _ := exp["queries"].([]any)
	if len(queries) != 1 || queries[0] != "oldQuery" {
		t.Errorf("stale expose was replaced with the newly discovered surface: %v", exp)
	}
	for _, q := range queries {
		if q == "secretAdmin" {
			t.Error("merge exposed an operation the user never approved")
		}
	}
	if !hasGap(readReport(t, out).Gaps, gapExposeStale, true) {
		t.Errorf("refusing to update must be reported as blocking, got %+v", readReport(t, out).Gaps)
	}
}

// A merge that re-derives an entry must not drop groups/output/selection — nor
// any other key a user added that this tool's struct does not model.
func TestExecuteMergePreservesHandWrittenPolicy(t *testing.T) {
	in := inputDir(t, "api/openapi.yaml", specOpenAPI)
	out := t.TempDir()
	if err := Execute(Options{Inputs: []string{in}, Out: out, Merge: true}); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(out, sourcesFileName)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	edited := strings.TrimRight(string(raw), "\n") + `
        display_name: Billing
        groups:
            - name: admin
        output: table
        selection: id
`
	if err := os.WriteFile(path, []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := Execute(Options{Inputs: []string{in}, Out: out, Merge: true}); err != nil {
		t.Fatal(err)
	}
	s := firstSource(t, path)
	for _, k := range []string{"display_name", "groups", "output", "selection"} {
		if s[k] == nil {
			t.Errorf("--merge dropped hand-written %q: %v", k, s)
		}
	}
	// Scan still re-derives what it owns.
	if s["backend"] != "openapi3" {
		t.Errorf("backend was not re-derived: %v", s["backend"])
	}
}

// An empty expose cannot generate; neither widening it nor writing it back as
// a usable source is acceptable.
func TestExecuteMergeEmptyExposeIsNotUsable(t *testing.T) {
	in := inputDir(t, "schema.graphql", sdlConsole)
	out := t.TempDir()
	if err := Execute(Options{Inputs: []string{in}, Out: out, Merge: true}); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(out, sourcesFileName)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	head := strings.Index(string(raw), "            expose:")
	if head < 0 {
		t.Fatalf("expose block not found in:\n%s", raw)
	}
	if err := os.WriteFile(path, []byte(string(raw)[:head]+"            expose: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	err = Execute(Options{Inputs: []string{in}, Out: out, Merge: true})
	var noSrc ErrNoSources
	if !errors.As(err, &noSrc) {
		t.Fatalf("an empty expose cannot yield a usable source; want ErrNoSources, got %v", err)
	}
	rep := readReport(t, out)
	if len(rep.Sources) != 0 || rep.Summary.Usable != 0 || rep.Summary.ExitCode != 2 {
		t.Errorf("empty expose still counted as usable: sources=%d usable=%d exit=%d",
			len(rep.Sources), rep.Summary.Usable, rep.Summary.ExitCode)
	}
	if !hasGap(rep.Gaps, gapExposeEmpty, true) {
		t.Errorf("expected blocking %s gap, got %+v", gapExposeEmpty, rep.Gaps)
	}
}

// A different spec inheriting a freed name must not inherit the old API's
// hand-written policy: policy follows provenance, never the name.
func TestExecuteMergePolicyFollowsProvenanceNotName(t *testing.T) {
	spec := func(path, op string) string {
		return "openapi: 3.0.3\ninfo: {title: Svc, version: \"1\"}\npaths:\n  " + path +
			":\n    get: {operationId: " + op + ", responses: {\"200\": {description: ok}}}\n"
	}
	in := t.TempDir()
	writeFile(t, in, "old/openapi.yaml", spec("/old-api", "oldOp"))
	out := t.TempDir()
	if err := Execute(Options{Inputs: []string{in}, Out: out, Merge: true}); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(out, sourcesFileName)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	edited := strings.TrimRight(string(raw), "\n") + `
        groups:
            - name: legacy-admin
        selection: oldField
`
	if err := os.WriteFile(path, []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := os.RemoveAll(filepath.Join(in, "old")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, in, "new/openapi.yaml", spec("/brand-new", "newOp"))
	if err := Execute(Options{Inputs: []string{in}, Out: out, Merge: true}); err != nil {
		t.Fatal(err)
	}

	s := firstSource(t, path)
	for _, k := range []string{"groups", "selection"} {
		if s[k] != nil {
			t.Errorf("policy for the removed API leaked onto a different one via its name: %s = %v", k, s[k])
		}
	}
	draft, err := os.ReadFile(filepath.Join(out, s["local_path"].(string), "openapi.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(draft), "/brand-new") {
		t.Errorf("expected the entry to describe the new API, got:\n%s", draft)
	}
}

// A source name must keep pointing at the same API when an unrelated one that
// sorts ahead of it shows up. A positional provenance key silently re-binds it.
func TestExecuteMergeIdentityStableAgainstInsertion(t *testing.T) {
	spec := func(path, op string) string {
		return "openapi: 3.0.3\ninfo: {title: Svc, version: \"1\"}\npaths:\n  " + path +
			":\n    get: {operationId: " + op + ", responses: {\"200\": {description: ok}}}\n"
	}
	in := t.TempDir()
	writeFile(t, in, "svc-a/api/openapi.yaml", spec("/alpha", "aOp"))
	writeFile(t, in, "svc-c/api/openapi.yaml", spec("/charlie", "cOp"))
	out := t.TempDir()
	if err := Execute(Options{Inputs: []string{in}, Out: out, Merge: true}); err != nil {
		t.Fatal(err)
	}

	bound := func() map[string]string {
		t.Helper()
		m := map[string]string{}
		for name := range readSources(t, filepath.Join(out, sourcesFileName)) {
			data, err := os.ReadFile(filepath.Join(out, name, "openapi.yaml"))
			if err != nil {
				continue
			}
			for _, p := range []string{"/alpha", "/bravo", "/charlie"} {
				if strings.Contains(string(data), p+":") {
					m[name] = p
				}
			}
		}
		return m
	}
	before := bound()

	writeFile(t, in, "svc-b/api/openapi.yaml", spec("/bravo", "bOp"))
	if err := Execute(Options{Inputs: []string{in}, Out: out, Merge: true}); err != nil {
		t.Fatal(err)
	}
	after := bound()

	for name, api := range before {
		if after[name] != api {
			t.Errorf("source %q was bound to %s and now points at %s", name, api, after[name])
		}
	}
}

func TestExecuteRejectsUnknownPrefer(t *testing.T) {
	err := Execute(Options{Inputs: []string{t.TempDir()}, Out: t.TempDir(), Prefer: "grpc"})
	if err == nil || !strings.Contains(err.Error(), "--prefer") {
		t.Fatalf("want a --prefer usage error, got %v", err)
	}
}

func TestExecutePreferPicksRecommended(t *testing.T) {
	recommendedBackend := func(prefer string) string {
		in := t.TempDir()
		writeFile(t, in, "api/openapi.yaml", specOneOp)
		writeFile(t, in, "schema.graphql", sdlOneQuery)
		out := t.TempDir()
		if err := Execute(Options{Inputs: []string{in}, Out: out, Prefer: prefer}); err != nil {
			t.Fatal(err)
		}
		for _, s := range readReport(t, out).Sources {
			if s.Recommended {
				return s.Backend
			}
		}
		return ""
	}
	if got := recommendedBackend(""); got != "openapi3" {
		t.Errorf("default recommendation = %q, want openapi3", got)
	}
	if got := recommendedBackend("graphql"); got != "graphql" {
		t.Errorf("--prefer graphql recommendation = %q, want graphql", got)
	}
}

// Passing the same tree twice is a user slip, not a request for two sources.
func TestExecuteDeduplicatesRepeatedInput(t *testing.T) {
	in := inputDir(t, "openapi.yaml", specOpenAPI)
	out := t.TempDir()
	if err := Execute(Options{Inputs: []string{in, in}, Out: out}); err != nil {
		t.Fatal(err)
	}
	if srcs := readSources(t, filepath.Join(out, sourcesFileName)); len(srcs) != 1 {
		t.Fatalf("repeated input produced %d sources: %v", len(srcs), srcs)
	}
}

// An input that cannot be scanned must surface as a blocking gap, not vanish.
func TestExecuteReportsInputError(t *testing.T) {
	good := inputDir(t, "openapi.yaml", specOpenAPI)
	missing := filepath.Join(t.TempDir(), "nope")
	out := t.TempDir()
	if err := Execute(Options{Inputs: []string{good, missing}, Out: out}); err != nil {
		t.Fatal(err)
	}
	if !hasGap(readReport(t, out).Gaps, gapInputError, true) {
		t.Errorf("unscannable input did not produce a blocking %s gap", gapInputError)
	}
}

func readReport(t *testing.T, out string) *Report {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(out, reportFileName))
	if err != nil {
		t.Fatal(err)
	}
	var r Report
	if err := json.Unmarshal(data, &r); err != nil {
		t.Fatalf("parse report: %v", err)
	}
	return &r
}

func firstSource(t *testing.T, sourcesPath string) map[string]any {
	t.Helper()
	srcs := readSources(t, sourcesPath)
	if len(srcs) != 1 {
		t.Fatalf("want exactly 1 source, got %d: %v", len(srcs), srcs)
	}
	for _, v := range srcs {
		return v
	}
	return nil
}

func hasGap(gaps []Gap, kind string, blocking bool) bool {
	for _, g := range gaps {
		if g.Kind == kind && g.Blocking == blocking {
			return true
		}
	}
	return false
}
