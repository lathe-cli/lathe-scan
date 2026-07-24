package scan

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Dependency/build trees are skipped: specs shipped by deps are the main false
// positives, so this is a correctness requirement, not an optimization.
var ignoreDirs = map[string]bool{
	"node_modules": true, "vendor": true, ".venv": true, "venv": true,
	"dist": true, "build": true, "target": true, ".git": true,
	"site-packages": true, ".tox": true, ".cache": true, "testdata": true,
}

var specDirHints = map[string]bool{
	"docs": true, "api": true, "openapi": true, "spec": true, "apidocs": true,
}

const maxSpecBytes = 32 << 20 // guard against pathological files

type inputResult struct {
	report  InputReport
	sources []*builtSource
}

func scanInput(input, scanPath, kindHint string, opts Options) (*inputResult, error) {
	abs, err := filepath.Abs(scanPath)
	if err != nil {
		return nil, fmt.Errorf("resolve %q: %w", input, err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return nil, fmt.Errorf("stat %q: %w", input, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("input is not a directory: %s", input)
	}

	// Zip is an extracted snapshot: always local_path, never a pinnable repo.
	var git *gitOrigin
	kind, root := "dir", abs
	if kindHint == "zip" {
		kind = "zip"
	} else if git = detectGitOrigin(abs); git != nil {
		kind, root = "git", git.root
	}

	ir := &inputResult{report: InputReport{Input: input, Kind: kind}}
	if git != nil {
		ir.report.Origin = &Origin{Type: "repo_url", RepoURL: git.repoURL, PinnedTag: git.pinnedTag, RefKind: git.refKind}
	} else {
		ir.report.Origin = &Origin{Type: "local_path", LocalPath: abs}
	}

	cands, parsedByPath := parseCandidates(discover(abs), root)
	dedupCandidates(cands, parsedByPath)
	ir.report.Candidates = append(ir.report.Candidates, cands...)
	for i := range cands {
		c := &cands[i]
		if !c.Parsed || c.DuplicateOf != "" {
			continue
		}
		b := buildSource(c, parsedByPath[c.Path], root, git)
		b.fromInput = input
		ir.sources = append(ir.sources, b)
	}

	if gfiles := discoverGraphQL(abs); len(gfiles) > 0 {
		b, cand := buildGraphQLSource(gfiles, root, git)
		if cand != nil {
			ir.report.Candidates = append(ir.report.Candidates, *cand)
		}
		if b != nil {
			b.fromInput = input
			ir.sources = append(ir.sources, b)
		}
	}

	if pfiles := discoverProto(abs); len(pfiles) > 0 {
		b, cand := buildProtoSource(pfiles, root, git)
		if cand != nil {
			ir.report.Candidates = append(ir.report.Candidates, *cand)
		}
		if b != nil {
			b.fromInput = input
			ir.sources = append(ir.sources, b)
		}
	}

	if pmSources, pmCands := buildPostmanSources(discoverPostman(abs), root); len(pmSources) > 0 || len(pmCands) > 0 {
		ir.report.Candidates = append(ir.report.Candidates, pmCands...)
		for _, b := range pmSources {
			b.fromInput = input
			ir.sources = append(ir.sources, b)
		}
	}

	// L2 only when L1 produced nothing usable.
	if !anyUsable(ir.sources) {
		if b, cand := runL2(abs, input); b != nil {
			if cand != nil {
				ir.report.Candidates = append(ir.report.Candidates, *cand)
			}
			b.fromInput = input
			ir.sources = append(ir.sources, b)
		}
	}

	if best := recommend(ir.sources); best != nil {
		best.report.Recommended = true
	}
	return ir, nil
}

func anyUsable(sources []*builtSource) bool {
	for _, b := range sources {
		if b.report.WouldEmitCommands > 0 && !hasBlocking(b.report.Gaps) {
			return true
		}
	}
	return false
}

func readCapped(path string) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.Size() > maxSpecBytes {
		return nil, fmt.Errorf("file too large: %s", path)
	}
	return os.ReadFile(path)
}

func recommend(sources []*builtSource) *builtSource {
	prio := map[string]int{"openapi3": 4, "swagger": 3, "graphql": 2, "proto": 1}
	var best *builtSource
	for _, b := range sources {
		switch {
		case best == nil:
			best = b
		case b.report.WouldEmitCommands != best.report.WouldEmitCommands:
			if b.report.WouldEmitCommands > best.report.WouldEmitCommands {
				best = b
			}
		case prio[b.yc.Backend] != prio[best.yc.Backend]:
			if prio[b.yc.Backend] > prio[best.yc.Backend] {
				best = b
			}
		case b.baseName < best.baseName:
			best = b
		}
	}
	return best
}

func walkFiles(rootDir string, keep func(path string) bool) []string {
	var out []string
	_ = filepath.WalkDir(rootDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			name := d.Name()
			if path != rootDir && (ignoreDirs[name] || (strings.HasPrefix(name, ".") && name != ".")) {
				return fs.SkipDir
			}
			return nil
		}
		if keep(path) {
			out = append(out, path)
		}
		return nil
	})
	sort.Strings(out)
	return out
}

func discover(rootDir string) []string      { return walkFiles(rootDir, looksLikeSpecFile) }
func discoverProto(rootDir string) []string { return walkFiles(rootDir, isProtoFile) }
func discoverGraphQL(rootDir string) []string {
	return walkFiles(rootDir, isGraphQLFile)
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

func isProtoFile(path string) bool {
	return strings.EqualFold(filepath.Ext(path), ".proto")
}

func isGraphQLFile(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	return ext == ".graphql" || ext == ".graphqls" || ext == ".gql"
}

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

func dedupCandidates(cands []Candidate, parsedByPath map[string]*parsed) {
	seen := map[string]string{}
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
		s++
	}
	return s
}

func reason(p *parsed) string {
	return fmt.Sprintf("%s, %d paths, %d operations, %d schemas",
		p.format, p.metrics.Paths, p.metrics.Operations, p.metrics.Schemas)
}
