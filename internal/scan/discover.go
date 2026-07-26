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
	"site-packages": true, ".tox": true, ".cache": true,
	// Test scaffolding and generated/sample trees: a spec found here is fixture
	// data, not the repo's own API contract. Excluding them is the dominant
	// precision win on real repos (e.g. openapi-generator ships 120+ sample specs).
	"testdata": true, "test": true, "tests": true, "__tests__": true,
	"e2e": true, "fixture": true, "fixtures": true,
	"sample": true, "samples": true, "example": true, "examples": true,
	"third_party": true, "third-party": true, "generated": true,
}

var specDirHints = map[string]bool{
	"docs": true, "api": true, "openapi": true, "spec": true, "apidocs": true,
}

const maxSpecBytes = 32 << 20 // guard against pathological files

type inputResult struct {
	report            InputReport
	sources           []*builtSource
	gaps              []Gap
	postmanCandidates int
}

// fileIndex is the result of the single tree walk each input gets. Every
// detector reads from it instead of re-walking, so discovery stays one pass.
type fileIndex struct {
	specs     []string
	protos    []string
	graphql   []string
	jsons     []string
	sources   []string // L2 candidates, capped at l2MaxFiles
	truncated bool     // source set hit the cap; L2 saw only a prefix
}

func indexFiles(rootDir string) *fileIndex {
	idx := &fileIndex{}
	var st ignoreStack
	st.seedParents(rootDir)
	_ = filepath.WalkDir(rootDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			name := d.Name()
			if path != rootDir && (ignoreDirs[name] || strings.HasPrefix(name, ".")) {
				return fs.SkipDir
			}
			st.enter(path)
			if path != rootDir && st.ignored(path, true) {
				return fs.SkipDir
			}
			return nil
		}
		if st.ignored(path, false) {
			return nil
		}
		switch {
		case isProtoFile(path):
			idx.protos = append(idx.protos, path)
		case isGraphQLFile(path):
			idx.graphql = append(idx.graphql, path)
		case isSourceFile(path):
			idx.sources = append(idx.sources, path)
		}
		if isJSONFile(path) {
			idx.jsons = append(idx.jsons, path)
		}
		if looksLikeSpecFile(path) {
			idx.specs = append(idx.specs, path)
		}
		return nil
	})
	for _, l := range []*[]string{&idx.specs, &idx.protos, &idx.graphql, &idx.jsons, &idx.sources} {
		sort.Strings(*l)
	}
	if len(idx.sources) > l2MaxFiles {
		idx.sources = idx.sources[:l2MaxFiles]
		idx.truncated = true
	}
	return idx
}

func scanInput(input, inputKey, scanPath, kindHint string, opts Options) (*inputResult, error) {
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
	// Every later boundary check compares physical paths, and git reports a
	// physical toplevel; resolving once here keeps the two in the same space.
	if abs, err = physicalRoot(abs); err != nil {
		return nil, fmt.Errorf("resolve %q: %w", input, err)
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
		// A worktree we could not pin still scans fine, but the result is not
		// reproducible from the origin alone — say so instead of staying silent.
		if kind == "dir" && isGitWorktree(abs) {
			ir.gaps = append(ir.gaps, Gap{Kind: gapNoImmutableRef, Scope: "input", Ref: input,
				Message:  "git worktree has no remote or no immutable ref at HEAD; emitted local_path instead of repo_url + pinned_tag",
				Blocking: false})
		}
	}

	idx := indexFiles(abs)

	cands, parsedByPath := parseCandidates(idx.specs, root)
	dedupCandidates(cands, parsedByPath)
	ir.report.Candidates = append(ir.report.Candidates, cands...)
	for i := range cands {
		c := &cands[i]
		if c.DuplicateOf != "" {
			continue
		}
		if !c.Parsed {
			// The usual reason a scan comes back empty; the human reads GAPS.md,
			// not candidates[] in the JSON.
			ir.gaps = append(ir.gaps, Gap{Kind: gapParseError, Scope: "input", Ref: c.Path,
				Message: c.Error, Blocking: true})
			continue
		}
		ir.add(buildSource(c, parsedByPath[c.Path], root, git), input)
	}

	if len(idx.graphql) > 0 {
		b, cand := buildGraphQLSource(idx.graphql, root, git)
		if cand != nil {
			ir.report.Candidates = append(ir.report.Candidates, *cand)
		}
		ir.add(b, input)
	}

	if len(idx.protos) > 0 {
		b, cand, gaps := buildProtoSource(idx.protos, root, git)
		if cand != nil {
			ir.report.Candidates = append(ir.report.Candidates, *cand)
		}
		ir.gaps = append(ir.gaps, gaps...)
		ir.add(b, input)
	}

	pmSources, pmCands := buildPostmanSources(postmanFiles(idx, root), root)
	ir.report.Candidates = append(ir.report.Candidates, pmCands...)
	ir.postmanCandidates = len(pmCands)
	for _, b := range pmSources {
		ir.add(b, input)
	}

	// L2 only when L1 produced nothing usable.
	ir.dropBlocked()
	if len(ir.sources) == 0 {
		b, cand := runL2(idx, input, abs)
		if cand != nil {
			ir.report.Candidates = append(ir.report.Candidates, *cand)
		}
		ir.add(b, input)
		ir.dropBlocked()
		// "Found nothing" and "only looked at part of it" are different answers,
		// and with no source there is nothing to carry the truncation gap.
		if len(ir.sources) == 0 && idx.truncated {
			ir.gaps = append(ir.gaps, Gap{Kind: gapScanTruncated, Scope: "input", Ref: input,
				Message:  fmt.Sprintf("only the first %d source files were analyzed and no routes were found among them; any defined beyond the cap were never seen", l2MaxFiles),
				Blocking: true})
		}
	}

	for _, b := range ir.sources {
		b.inputKey = inputKey
	}
	if best := recommend(ir.sources, opts.Prefer); best != nil {
		best.report.Recommended = true
	}
	return ir, nil
}

func (ir *inputResult) add(b *builtSource, input string) {
	if b == nil {
		return
	}
	b.fromInput = input
	ir.sources = append(ir.sources, b)
}

// dropBlocked removes sources Lathe would reject or generate nothing from and
// promotes their blocking gaps to the report's top level: emitting them hands
// back an ungeneratable manifest, dropping them silently hides why.
func (ir *inputResult) dropBlocked() {
	kept := ir.sources[:0]
	for _, b := range ir.sources {
		if !hasBlocking(b.report.Gaps) {
			kept = append(kept, b)
			continue
		}
		for _, g := range b.report.Gaps {
			if !g.Blocking {
				continue
			}
			g.Scope = "source"
			g.Ref = b.baseName
			ir.gaps = append(ir.gaps, g)
		}
	}
	ir.sources = kept
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

// recommend picks one source per input. --prefer breaks ties on backend ahead of
// the built-in priority, but never overrides a source that would emit more
// commands: a preferred backend that generates less is still the worse choice.
func recommend(sources []*builtSource, prefer string) *builtSource {
	prio := map[string]int{"openapi3": 4, "swagger": 3, "graphql": 2, "proto": 1}
	rank := func(b *builtSource) (int, int) {
		preferred := 0
		if prefer != "" && b.yc.Backend == prefer {
			preferred = 1
		}
		return preferred, prio[b.yc.Backend]
	}
	var best *builtSource
	for _, b := range sources {
		if best == nil {
			best = b
			continue
		}
		bPref, bPrio := rank(b)
		cPref, cPrio := rank(best)
		switch {
		case b.report.WouldEmitCommands != best.report.WouldEmitCommands:
			if b.report.WouldEmitCommands > best.report.WouldEmitCommands {
				best = b
			}
		case bPref != cPref:
			if bPref > cPref {
				best = b
			}
		case bPrio != cPrio:
			if bPrio > cPrio {
				best = b
			}
		case b.baseName < best.baseName:
			best = b
		}
	}
	return best
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
		data, err := readWithin(root, f)
		if err != nil {
			// A candidate that resolves outside the tree is refused, not skipped:
			// silently dropping it reads as "there was nothing there".
			if !pathWithin(root, f) {
				cands = append(cands, Candidate{Path: rel, Format: "unknown", Parsed: false, Error: err.Error()})
			}
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
		if !c.Parsed {
			continue
		}
		// Prefer the operation-signature key so json/yaml copies of one API
		// collapse; fall back to content hash when there are no operations.
		key := c.ContentHash
		if p := parsedByPath[c.Path]; p != nil && p.opsig != "" {
			key = "sig:" + p.opsig
		}
		if key == "" {
			continue
		}
		if canon, ok := seen[key]; ok {
			c.DuplicateOf = canon
			delete(parsedByPath, c.Path)
			continue
		}
		seen[key] = c.Path
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
