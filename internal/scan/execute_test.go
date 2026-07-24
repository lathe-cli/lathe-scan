package scan

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"
)

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

// inputDir writes a spec into a fresh non-git temp dir and returns the dir.
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
	s := srcs["billing-api"]
	if s == nil {
		t.Fatalf("source billing-api missing, got %v", srcs)
	}
	if s["backend"] != "openapi3" {
		t.Errorf("backend = %v", s["backend"])
	}
	if s["local_path"] != "billing-api" {
		t.Errorf("local_path = %v, want billing-api", s["local_path"])
	}
	if s["repo_url"] != nil {
		t.Errorf("local source must not emit repo_url, got %v", s["repo_url"])
	}
	if s["default_hostname"] != "api.acme.com" {
		t.Errorf("default_hostname = %v", s["default_hostname"])
	}
	// The spec file must be copied under <out>/<name>/.
	if _, err := os.Stat(filepath.Join(out, "billing-api", "openapi.yaml")); err != nil {
		t.Errorf("copied spec missing: %v", err)
	}
	// report.json + GAPS.md written.
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

func TestExecuteZeroOperationsBlocks(t *testing.T) {
	spec := "openapi: 3.0.0\ninfo:\n  title: Empty API\npaths: {}\n"
	in := inputDir(t, "openapi.yaml", spec)
	out := t.TempDir()
	if err := Execute(Options{Inputs: []string{in}, Out: out}); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	rep := readReport(t, out)
	if len(rep.Sources) != 1 {
		t.Fatalf("want 1 source, got %d", len(rep.Sources))
	}
	s := rep.Sources[0]
	if s.WouldEmitCommands != 0 {
		t.Errorf("wouldEmit = %d, want 0", s.WouldEmitCommands)
	}
	if s.Confidence != confLow {
		t.Errorf("confidence = %q, want low", s.Confidence)
	}
	if !hasGap(s.Gaps, gapParseError, true) {
		t.Errorf("expected blocking %s gap, got %+v", gapParseError, s.Gaps)
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
	// A pre-existing graphql source (a backend this slice does not emit).
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
	if _, ok := srcs["billing-api"]; !ok {
		t.Error("--merge did not add the new source")
	}
	// Foreign graphql block must survive intact.
	g, _ := srcs["legacy-graph"]["graphql"].(map[string]any)
	if g == nil || g["schema"] != "schema.graphql" {
		t.Errorf("foreign graphql block corrupted: %v", srcs["legacy-graph"])
	}
}

// helpers

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

func hasGap(gaps []Gap, kind string, blocking bool) bool {
	for _, g := range gaps {
		if g.Kind == kind && g.Blocking == blocking {
			return true
		}
	}
	return false
}
