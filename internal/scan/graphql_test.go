package scan

import (
	"path/filepath"
	"testing"
)

const sdlConsole = `type Query {
  listApps: [App!]!
  getApp(id: ID!): App
}
type Mutation {
  createApp(name: String!): App
}
type App { id: ID! name: String! }
`

func TestBuildGraphQLSource(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "schema.graphql", sdlConsole)

	b, cand := buildGraphQLSource([]string{filepath.Join(dir, "schema.graphql")}, dir, nil)
	if cand == nil || !cand.Parsed {
		t.Fatalf("candidate not parsed: %+v", cand)
	}
	if b == nil {
		t.Fatal("expected a graphql source")
	}
	if b.yc.Backend != "graphql" {
		t.Errorf("backend = %q", b.yc.Backend)
	}
	got := b.yc.GraphQL.Expose
	if !equalSlice(got.Queries, []string{"getApp", "listApps"}) {
		t.Errorf("queries = %v, want [getApp listApps]", got.Queries)
	}
	if !equalSlice(got.Mutations, []string{"createApp"}) {
		t.Errorf("mutations = %v, want [createApp]", got.Mutations)
	}
	if b.report.WouldEmitCommands != 3 {
		t.Errorf("wouldEmit = %d, want 3", b.report.WouldEmitCommands)
	}
	if b.report.Confidence != confHigh {
		t.Errorf("confidence = %q, want high", b.report.Confidence)
	}
	// Exposing everything is a policy the human must confirm — advisory, not blocking.
	if !hasGap(b.report.Gaps, gapGraphQLExpose, false) {
		t.Errorf("expected advisory expose gap, got %+v", b.report.Gaps)
	}
}

func TestGraphQLSplitSchemaBlocks(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "root.graphql", "type Query { app: App }\n")
	writeFile(t, dir, "types.graphql", "type App { id: ID! }\n")

	b, _ := buildGraphQLSource(
		[]string{filepath.Join(dir, "root.graphql"), filepath.Join(dir, "types.graphql")},
		dir, nil)
	if b == nil {
		t.Fatal("expected a source")
	}
	if !hasGap(b.report.Gaps, gapGraphQLSplit, true) {
		t.Errorf("expected blocking split-schema gap, got %+v", b.report.Gaps)
	}
	if b.report.Confidence != confLow {
		t.Errorf("split schema confidence = %q, want low", b.report.Confidence)
	}
}

func TestExecuteGraphQL(t *testing.T) {
	in := inputDir(t, "schema.graphql", sdlConsole)
	out := t.TempDir()
	if err := Execute(Options{Inputs: []string{in}, Out: out}); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	s := firstSource(t, filepath.Join(out, sourcesFileName))
	if s["backend"] != "graphql" {
		t.Fatalf("backend = %v", s["backend"])
	}
	g, _ := s["graphql"].(map[string]any)
	if g == nil || g["schema"] != "schema.graphql" {
		t.Errorf("graphql.schema wrong: %v", s["graphql"])
	}
	exp, _ := g["expose"].(map[string]any)
	if exp == nil || exp["queries"] == nil {
		t.Errorf("expose.queries missing: %v", g)
	}
}

func equalSlice(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
