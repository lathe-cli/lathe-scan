package scan

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Dependency, build, and generated trees are always skipped: specs shipped by
// deps are the main false positives, so this is a correctness requirement, not
// an optimization.
var dependencyDirs = map[string]bool{
	"node_modules": true, "vendor": true, ".venv": true, "venv": true,
	"dist": true, "build": true, "target": true, ".git": true,
	"site-packages": true, ".tox": true, ".cache": true,
	"third_party": true, "third-party": true, "generated": true,
}

// Test scaffolding and sample trees: a spec found here is fixture data, not
// the repo's own API contract. Excluding them is the dominant precision win on
// real repos (e.g. openapi-generator ships 120+ sample specs) — except under a
// src/main source root, where JVM package directories reuse these words as
// production namespace segments (org.springframework.samples, com.example)
// and the Maven/Gradle convention already guarantees the tree is real code.
var fixtureDirs = map[string]bool{
	"testdata": true, "test": true, "tests": true, "__tests__": true,
	"e2e": true, "fixture": true, "fixtures": true,
	"sample": true, "samples": true, "example": true, "examples": true,
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
	specs     []string // name- or directory-hinted spec files
	yamls     []string // every YAML file, probed by content for unhinted specs
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
			if path != rootDir && (dependencyDirs[name] || strings.HasPrefix(name, ".") ||
				fixtureDirs[name] && !underSourceRoot(rootDir, path)) {
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
		if isYAMLFile(path) {
			idx.yamls = append(idx.yamls, path)
		}
		if looksLikeSpecFile(path) {
			idx.specs = append(idx.specs, path)
		}
		return nil
	})
	for _, l := range []*[]string{&idx.specs, &idx.yamls, &idx.protos, &idx.graphql, &idx.jsons, &idx.sources} {
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

	cands, parsedByPath := parseCandidates(specCandidateFiles(idx, root), root)
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
			// A schema that fails strict SDL validation fails identically in
			// Lathe (same parser); an empty result must say so, not stay silent.
			if !cand.Parsed {
				ir.gaps = append(ir.gaps, Gap{Kind: gapParseError, Scope: "input", Ref: cand.Path,
					Message: cand.Error, Blocking: true})
			}
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
	for _, c := range pmCands {
		if !c.Parsed {
			ir.gaps = append(ir.gaps, Gap{Kind: gapParseError, Scope: "input", Ref: c.Path,
				Message: c.Error, Blocking: true})
		}
	}
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

	groups := map[string][]*builtSource{}
	for _, b := range ir.sources {
		b.inputKey = inputKey
		groups[b.groupKey()] = append(groups[b.groupKey()], b)
	}
	for _, group := range groups {
		recommend(group, opts.Prefer).report.Recommended = true
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

// recommend picks one source from a logical-source group. --prefer breaks ties
// on backend ahead of the built-in priority, but never overrides a source that would emit more
// commands: a preferred backend that generates less is still the worse choice.
func recommend(sources []*builtSource, prefer string) *builtSource {
	prio := map[string]int{"openapi3": 4, "swagger": 3, "graphql": 2, "proto": 1}
	best := sources[0]
	for _, b := range sources[1:] {
		switch {
		case b.report.WouldEmitCommands != best.report.WouldEmitCommands:
			if b.report.WouldEmitCommands > best.report.WouldEmitCommands {
				best = b
			}
		case prefer != "" && (b.yc.Backend == prefer) != (best.yc.Backend == prefer):
			if b.yc.Backend == prefer {
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

// specCandidateFiles merges the name/directory-hinted spec files with every
// other YAML or JSON document whose content mentions an openapi/swagger key.
// Content is what finds a spec named petstore.yaml at the repository root; a
// name-only heuristic silently misses every spec whose author did not follow
// the openapi*/swagger* convention. The substring gate keeps the walk from
// YAML-parsing every Kubernetes manifest and lockfile on the way there.
func specCandidateFiles(idx *fileIndex, root string) []string {
	seen := make(map[string]bool, len(idx.specs))
	files := append([]string(nil), idx.specs...)
	for _, f := range idx.specs {
		seen[f] = true
	}
	for _, list := range [][]string{idx.yamls, idx.jsons} {
		for _, f := range list {
			if seen[f] {
				continue
			}
			seen[f] = true
			data, err := readWithin(root, f)
			if err != nil {
				continue
			}
			if probesAsSpec(data) {
				files = append(files, f)
			}
		}
	}
	sort.Strings(files)
	return files
}

// probesAsSpec is the cheap gate before a real parse: a Lathe-native spec must
// carry an openapi or swagger version key, as a YAML key or a JSON string key.
// A false positive costs one parse attempt that parseSpec rejects quietly; a
// file without either token cannot be a recognizable spec at all.
func probesAsSpec(data []byte) bool {
	s := string(data)
	return strings.Contains(s, "openapi:") || strings.Contains(s, `"openapi"`) ||
		strings.Contains(s, "swagger:") || strings.Contains(s, `"swagger"`)
}

func isYAMLFile(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	return ext == ".yaml" || ext == ".yml"
}

// underSourceRoot reports whether path sits below a Maven/Gradle-style
// src/main source root inside rootDir. Fixture-named directories below it are
// package segments of production code, not scaffolding.
func underSourceRoot(rootDir, path string) bool {
	rel, err := filepath.Rel(rootDir, path)
	if err != nil {
		return false
	}
	return strings.Contains("/"+filepath.ToSlash(rel), "/src/main/")
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
		rel := repoRelativePath(root, f)
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

func repoRelativePath(root, path string) string {
	physicalPath, ok := resolveWithin(root, path)
	if !ok {
		return ""
	}
	physicalRoot, err := physicalRoot(root)
	if err != nil {
		return ""
	}
	rel, err := filepath.Rel(physicalRoot, physicalPath)
	if err != nil {
		return ""
	}
	return filepath.ToSlash(rel)
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
