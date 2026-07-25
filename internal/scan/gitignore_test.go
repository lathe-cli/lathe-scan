package scan

import (
	"path/filepath"
	"testing"
)

func TestIgnorePatternMatching(t *testing.T) {
	cases := []struct {
		pattern string
		path    string
		isDir   bool
		want    bool
	}{
		{"gen/", "gen", true, true},
		{"gen/", "gen", false, false}, // dir-only never matches a file
		{"gen/", "pkg/gen", true, true},
		{"/gen", "gen", true, true},
		{"/gen", "pkg/gen", true, false}, // leading slash anchors to the .gitignore dir
		{"*.log", "a/b/c.log", false, true},
		{"*.log", "a/b/c.txt", false, false},
		{"docs/*.yaml", "docs/openapi.yaml", false, true},
		{"docs/*.yaml", "src/docs/openapi.yaml", false, false}, // interior slash anchors
		{"**/build", "a/b/build", true, true},
		{"api?.json", "api1.json", false, true},
		{"api[0-9].json", "api7.json", false, true},
		{"api[!0-9].json", "api7.json", false, false},
		{"node_modules", "node_modules/pkg/openapi.yaml", false, true}, // subtree follows the dir
	}
	for _, c := range cases {
		p, ok := compileIgnorePattern(c.pattern)
		if !ok {
			t.Fatalf("compileIgnorePattern(%q) failed", c.pattern)
		}
		got := p.re.MatchString(c.path) && (!p.dirOnly || c.isDir)
		if got != c.want {
			t.Errorf("pattern %q vs %q (dir=%v) = %v, want %v", c.pattern, c.path, c.isDir, got, c.want)
		}
	}
}

func TestIgnorePatternSkipsCommentsAndBlanks(t *testing.T) {
	for _, line := range []string{"", "   ", "# comment", "\t"} {
		if _, ok := compileIgnorePattern(line); ok {
			t.Errorf("compileIgnorePattern(%q) should be skipped", line)
		}
	}
}

// Later patterns win, which is how a .gitignore re-includes one file.
func TestIgnoreNegation(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, ".gitignore", "*.yaml\n!keep.yaml\n")
	var st ignoreStack
	st.enter(root)

	if !st.ignored(filepath.Join(root, "drop.yaml"), false) {
		t.Error("drop.yaml should be ignored")
	}
	if st.ignored(filepath.Join(root, "keep.yaml"), false) {
		t.Error("keep.yaml was re-included by ! and must not be ignored")
	}
}

func TestIgnoreNestedOverridesParent(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, ".gitignore", "*.yaml\n")
	writeFile(t, root, "svc/.gitignore", "!*.yaml\n")

	var st ignoreStack
	st.enter(root)
	if !st.ignored(filepath.Join(root, "a.yaml"), false) {
		t.Error("root pattern should ignore a.yaml")
	}
	st.enter(filepath.Join(root, "svc"))
	if st.ignored(filepath.Join(root, "svc", "a.yaml"), false) {
		t.Error("nested .gitignore should re-include svc/a.yaml")
	}
	// Leaving the subtree must drop its layer again.
	st.enter(root)
	if !st.ignored(filepath.Join(root, "a.yaml"), false) {
		t.Error("nested layer leaked after leaving svc/")
	}
}

// DESIGN: discovery respects .gitignore, so a spec the repo declares generated
// is not selected as that repo's API contract.
func TestDiscoverRespectsGitignore(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, ".gitignore", "gen/\nold-openapi.yaml\n")
	writeFile(t, root, "api/openapi.yaml", specOpenAPI)
	writeFile(t, root, "gen/openapi.yaml", specOpenAPI)
	writeFile(t, root, "old-openapi.yaml", specOpenAPI)

	got := indexFiles(root).specs
	if len(got) != 1 {
		t.Fatalf("want only the non-ignored spec, got %d: %v", len(got), got)
	}
	if filepath.Base(filepath.Dir(got[0])) != "api" {
		t.Errorf("discovered the wrong file: %s", got[0])
	}
}

// examples/ ships other people's specs, the same false positive samples/ does.
func TestDiscoverSkipsExampleDirs(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "openapi.yaml", specOpenAPI)
	for _, d := range []string{"example", "examples"} {
		writeFile(t, root, d+"/openapi.yaml", specOpenAPI)
	}
	if got := indexFiles(root).specs; len(got) != 1 || filepath.Dir(got[0]) != root {
		t.Fatalf("want only the root spec, got %d: %v", len(got), got)
	}
}
