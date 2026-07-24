package scan

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ignoreDirs are dependency/build/vendored trees skipped by default. Specs
// shipped by dependencies are the main source of false positives, so excluding
// them is a correctness requirement, not an optimization.
var ignoreDirs = map[string]bool{
	"node_modules": true, "vendor": true, ".venv": true, "venv": true,
	"dist": true, "build": true, "target": true, ".git": true,
	"site-packages": true, ".tox": true, ".cache": true, "testdata": true,
}

// specDirHints are directories where specs commonly live even without a
// spec-like filename.
var specDirHints = map[string]bool{
	"docs": true, "api": true, "openapi": true, "spec": true, "apidocs": true,
}

const maxSpecBytes = 32 << 20 // 32 MiB guard against pathological files

// builtSource is a fully resolved source ready to write into sources.yaml.
type builtSource struct {
	Name      string // assigned during collision resolution
	baseName  string
	fromInput string // original input arg this source came from

	backend  string
	origin   *Origin
	hostname string
	files    []string // paths recorded in the backend block
	copyFrom string   // abs path to copy for local_path sources ("" for repo_url)
	copyTo   string   // basename under <out>/<name>/ for local_path sources

	report *SourceReport
}

// inputResult is what one input contributed.
type inputResult struct {
	report  InputReport
	sources []*builtSource
}

// scanInput discovers, parses, dedups, and builds sources for one input.
func scanInput(input string, opts Options) (*inputResult, error) {
	abs, err := filepath.Abs(input)
	if err != nil {
		return nil, fmt.Errorf("resolve %q: %w", input, err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return nil, fmt.Errorf("stat %q: %w", input, err)
	}
	if !info.IsDir() {
		if strings.HasSuffix(strings.ToLower(abs), ".zip") {
			return nil, fmt.Errorf("zip input is not yet supported: %s", input)
		}
		return nil, fmt.Errorf("input is not a directory: %s", input)
	}

	git := detectGitOrigin(abs)
	kind := "dir"
	root := abs
	if git != nil {
		kind = "git"
		root = git.root
	}

	files := discover(abs)
	cands, parsedByPath := parseCandidates(files, root)
	dedupCandidates(cands, parsedByPath)

	ir := &inputResult{report: InputReport{Input: input, Kind: kind}}
	if git != nil {
		ir.report.Origin = &Origin{Type: "repo_url", RepoURL: git.repoURL, PinnedTag: git.pinnedTag, RefKind: git.refKind}
	} else {
		ir.report.Origin = &Origin{Type: "local_path", LocalPath: abs}
	}
	ir.report.Candidates = cands

	// Build a source per usable, non-duplicate candidate.
	var best *builtSource
	bestScore := -1
	for i := range cands {
		c := &cands[i]
		if !c.Parsed || c.DuplicateOf != "" {
			continue
		}
		p := parsedByPath[c.Path]
		b := buildSource(c, p, root, abs, git, opts)
		b.fromInput = input
		ir.sources = append(ir.sources, b)
		if c.Score > bestScore {
			bestScore, best = c.Score, b
		}
	}
	if best != nil {
		best.report.Recommended = true
	}
	return ir, nil
}

// discover walks a tree and returns candidate spec file paths (absolute).
func discover(rootDir string) []string {
	var out []string
	_ = filepath.WalkDir(rootDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			name := d.Name()
			if path != rootDir && (ignoreDirs[name] || strings.HasPrefix(name, ".") && name != ".") {
				return fs.SkipDir
			}
			return nil
		}
		if looksLikeSpecFile(path) {
			out = append(out, path)
		}
		return nil
	})
	sort.Strings(out)
	return out
}

func looksLikeSpecFile(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	if ext != ".yaml" && ext != ".yml" && ext != ".json" {
		return false
	}
	base := strings.ToLower(filepath.Base(path))
	if strings.HasPrefix(base, "openapi") || strings.HasPrefix(base, "swagger") {
		return true
	}
	parent := strings.ToLower(filepath.Base(filepath.Dir(path)))
	return specDirHints[parent]
}

// parseCandidates reads and parses each file, producing report Candidates and a
// path->parsed map for usable ones.
func parseCandidates(files []string, root string) ([]Candidate, map[string]*parsed) {
	var cands []Candidate
	parsedByPath := map[string]*parsed{}
	for _, f := range files {
		rel := relOrBase(root, f)
		data, err := os.ReadFile(f)
		if err != nil || len(data) > maxSpecBytes {
			continue
		}
		p, perr := parseSpec(data)
		switch {
		case p != nil:
			c := Candidate{
				Path: rel, Format: p.format, Parsed: true,
				ContentHash: p.contentHash,
				Score:       score(p),
				Metrics:     &Metrics{Paths: p.metrics.Paths, Operations: p.metrics.Operations, Schemas: p.metrics.Schemas},
				Reason:      reason(p),
			}
			cands = append(cands, c)
			parsedByPath[rel] = p
		case nameStrongMatch(f):
			msg := "unrecognized or invalid spec"
			if perr != nil {
				msg = perr.Error()
			}
			cands = append(cands, Candidate{Path: rel, Format: "unknown", Parsed: false, Error: msg})
		}
	}
	sort.Slice(cands, func(i, j int) bool { return cands[i].Path < cands[j].Path })
	return cands, parsedByPath
}

// dedupCandidates collapses identical content, keeping the first path (sorted).
func dedupCandidates(cands []Candidate, parsedByPath map[string]*parsed) {
	seen := map[string]string{} // hash -> canonical path
	for i := range cands {
		c := &cands[i]
		if !c.Parsed || c.ContentHash == "" {
			continue
		}
		if canon, ok := seen[c.ContentHash]; ok {
			c.DuplicateOf = canon
			delete(parsedByPath, c.Path)
			continue
		}
		seen[c.ContentHash] = c.Path
	}
}

func nameStrongMatch(path string) bool {
	base := strings.ToLower(filepath.Base(path))
	return strings.HasPrefix(base, "openapi") || strings.HasPrefix(base, "swagger")
}

func relOrBase(root, path string) string {
	if rel, err := filepath.Rel(root, path); err == nil && !strings.HasPrefix(rel, "..") {
		return filepath.ToSlash(rel)
	}
	return filepath.Base(path)
}

func score(p *parsed) int {
	s := p.metrics.Operations*10 + p.metrics.Paths*2 + p.metrics.Schemas
	if p.title != "" {
		s += 5
	}
	if p.format == "openapi3" {
		s++ // tie-break toward OpenAPI 3
	}
	return s
}

func reason(p *parsed) string {
	return fmt.Sprintf("%s, %d paths, %d operations, %d schemas",
		p.format, p.metrics.Paths, p.metrics.Operations, p.metrics.Schemas)
}
