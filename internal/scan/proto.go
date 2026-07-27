package scan

import (
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

var (
	reBlockComment = regexp.MustCompile(`(?s)/\*.*?\*/`)
	reLineComment  = regexp.MustCompile(`//[^\n]*`)
	reProtoImport  = regexp.MustCompile(`(?m)^\s*import\s+(?:public\s+|weak\s+)?"([^"]+)"\s*;`)
	reProtoService = regexp.MustCompile(`(?m)\bservice\s+\w+\s*\{`)
	// Header only: body starts at "{" or ends at ";".
	reProtoRPCHeader = regexp.MustCompile(`\brpc\s+(\w+)\s*\([^)]*\)\s*returns\s*\([^)]*\)\s*`)
)

type protoFileInfo struct {
	rel         string
	services    int
	methods     int
	httpMethods int
	imports     []string
}

// No protoc. Lathe only generates for RPCs with google.api.http, so would_emit
// counts those. Compilation is unverified offline → usable sources cap at medium.
func buildProtoSource(files []string, root string, git *gitOrigin) (*builtSource, *Candidate, []Gap) {
	parsedFiles := make([]string, 0, len(files))
	infos := make([]protoFileInfo, 0, len(files))
	dirs := make([]string, 0, len(files))
	var methods, httpMethods, services int
	var imports []string
	for _, f := range files {
		data, err := readWithin(root, f)
		if err != nil {
			continue
		}
		info := analyzeProto(repoRelativePath(root, f), data)
		parsedFiles = append(parsedFiles, f)
		infos = append(infos, info)
		dirs = append(dirs, filepath.Dir(f))
		methods += info.methods
		httpMethods += info.httpMethods
		services += info.services
		imports = append(imports, info.imports...)
	}
	if len(infos) == 0 {
		return nil, nil, nil
	}

	// Prefer a root under which local imports resolve; else common ancestor.
	protoDir := resolveProtoRoot(parsedFiles, imports, commonDir(dirs))
	cand := &Candidate{
		Path: repoRelativePath(root, protoDir), Format: "proto", Parsed: true,
		Metrics: &Metrics{Operations: methods},
		Reason:  fmt.Sprintf("proto, %d services, %d rpc, %d google.api.http", services, methods, httpMethods),
	}

	// Lathe generates commands only for RPCs with a google.api.http annotation.
	// A tree with none produces zero commands and, worse, fails `lathe specsync`
	// (protoc chokes on unresolved imports) which aborts the whole bootstrap. So
	// report it as a candidate but do not emit it as a source.
	if httpMethods == 0 {
		cand.Reason += "; not emitted (no google.api.http)"
		// Blocking: this is what kept the source out of sources.yaml; calling it
		// advisory would contradict the gap vocabulary.
		return nil, cand, []Gap{{Kind: gapProtoNoHTTP, Scope: "input", Ref: cand.Path,
			Message:  fmt.Sprintf("%d rpc across %d service(s) but no google.api.http annotation; Lathe would emit no commands, so no proto source was written", methods, services),
			Blocking: true}}
	}

	resolution := resolveProtoClosure(parsedFiles, infos, root)
	if len(resolution.missing) > 0 {
		gaps := make([]Gap, 0, len(resolution.missing))
		for _, missing := range resolution.missing {
			// Input scope, like the no-http refusal above: no source is written,
			// so there is no source entry for a source-scoped gap to hang on.
			gaps = append(gaps, Gap{
				Kind: gapRefUnresolved, Scope: "input", Ref: cand.Path,
				Message:  fmt.Sprintf("proto import %q from %s has no repository file or reproducibly pinned dependency", missing.path, missing.importer),
				Blocking: true,
			})
		}
		cand.Reason += "; not emitted (unresolved import closure)"
		return nil, cand, gaps
	}

	// One layout decision for the whole tree: paths are either relative to the
	// proto root or carry the Go module prefix. The relative path is checked
	// rather than joined blindly — filepath.Join would fold a "../sibling"
	// escape into a module path that names nothing.
	stageRoot, prefix := protoDir, ""
	if resolution.moduleMapped {
		stageRoot, prefix = resolution.moduleRoot, resolution.modulePath
	}
	stagedPath := func(file string) (string, bool) {
		rel, err := filepath.Rel(stageRoot, file)
		if err != nil {
			return "", false
		}
		staged := filepath.Join(prefix, rel)
		if !filepath.IsLocal(staged) {
			return "", false
		}
		return filepath.ToSlash(staged), true
	}

	closureSet := make(map[string]bool, len(resolution.closure))
	for _, file := range resolution.closure {
		closureSet[filepath.Clean(file)] = true
	}
	var entries []string
	for i, info := range infos {
		file := filepath.Clean(parsedFiles[i])
		if info.services == 0 || !closureSet[file] {
			continue
		}
		if rel, ok := stagedPath(file); ok {
			entries = append(entries, rel)
		}
	}
	// No services: still stage every reachable file so protoc has entrypoints.
	if len(entries) == 0 {
		for _, file := range resolution.closure {
			if rel, ok := stagedPath(file); ok {
				entries = append(entries, rel)
			}
		}
	}
	sort.Strings(entries)

	repoName := ""
	if git != nil {
		repoName = git.repoName
	}
	b := &builtSource{
		baseName:  firstNonEmpty(sanitizeName(repoName), sanitizeName(filepath.Base(protoDir)), "proto"),
		identity:  "proto",
		yc:        &ycSource{Backend: "proto"},
		inputRoot: root,
	}
	block := &protoBlock{Entries: entries, Dependencies: resolution.dependencies}
	if resolution.moduleMapped {
		block.ImportRoots = []string{resolution.modulePath}
	}
	// Evidence is the repository's own protos the entries reach, keyed
	// repo-relative. A vendored provider tree says nothing about where this API
	// lives, so it must not decide the source's location.
	own := make([]string, 0, len(resolution.closure))
	for _, file := range resolution.closure {
		if rel, err := filepath.Rel(root, file); err == nil {
			own = append(own, filepath.ToSlash(rel))
		}
	}
	sort.Strings(own)
	b.inputFiles = own

	// Pinning is a wider claim than evidence: the provider has to be fetchable
	// at the same ref, or `lathe sync-specs` compiles against a tree missing it.
	closure := append([]string(nil), own...)
	for _, provider := range resolution.vendored {
		if rel, err := filepath.Rel(root, provider.abs); err == nil {
			closure = append(closure, filepath.ToSlash(rel))
		}
	}
	sort.Strings(closure)
	pinned := git.pinnable(closure)
	if pinned {
		b.origin = &Origin{Type: "repo_url", RepoURL: git.repoURL, PinnedTag: git.pinnedTag, RefKind: git.refKind}
		b.yc.RepoURL = git.repoURL
		b.yc.PinnedTag = git.pinnedTag
		stageTo := "."
		if prefix != "" {
			stageTo = prefix
		}
		from := "."
		if rel, err := filepath.Rel(git.root, stageRoot); err == nil && rel != "." {
			from = filepath.ToSlash(rel)
		}
		block.Staging = []stagingEntry{{From: from, To: stageTo}}
		// A vendored provider tree stages at the sync root: protoc looks for it
		// under the import path exactly as the importing file wrote it.
		staged := map[stagingEntry]bool{block.Staging[0]: true}
		for _, vendorRoot := range resolution.vendorRoots {
			rel, err := filepath.Rel(git.root, vendorRoot)
			if err != nil {
				continue
			}
			entry := stagingEntry{From: filepath.ToSlash(rel), To: "."}
			if !staged[entry] {
				staged[entry] = true
				block.Staging = append(block.Staging, entry)
			}
		}
	} else {
		b.origin = &Origin{Type: "local_path"}
		block.Staging = []stagingEntry{{From: ".", To: "."}}
		for _, file := range resolution.closure {
			if rel, ok := stagedPath(file); ok {
				b.copies = append(b.copies, copyItem{absFrom: file, relTo: rel})
			}
		}
		for _, provider := range resolution.vendored {
			b.copies = append(b.copies, copyItem{absFrom: provider.abs, relTo: provider.rel})
		}
	}
	b.yc.Proto = block

	gaps := []Gap{{Kind: gapProtoImports, Scope: "source",
		Message: "staging and import roots were inferred statically; run `lathe sync-specs` (needs protoc) to verify the tree compiles", Blocking: false}}
	if g, ok := notAtRefGap(git, pinned); ok {
		gaps = append(gaps, g)
	}
	b.report = &SourceReport{
		Level: "L1", Backend: "proto",
		WouldEmitCommands: httpMethods,
		Files:             entries,
		Metrics:           &Metrics{Operations: methods},
		Gaps:              gaps,
		Confidence:        confMedium,
	}
	return b, cand, nil
}

func analyzeProto(rel string, data []byte) protoFileInfo {
	src := reLineComment.ReplaceAllString(reBlockComment.ReplaceAllString(string(data), ""), "")
	info := protoFileInfo{rel: rel}
	info.services = len(reProtoService.FindAllStringIndex(src, -1))
	for _, m := range reProtoImport.FindAllStringSubmatch(src, -1) {
		info.imports = append(info.imports, m[1])
	}

	for _, loc := range reProtoRPCHeader.FindAllStringIndex(src, -1) {
		info.methods++
		rest := strings.TrimLeft(src[loc[1]:], " \t\r\n")
		if strings.HasPrefix(rest, "{") {
			if body, ok := braceBlock(rest); ok && strings.Contains(body, "google.api.http") {
				info.httpMethods++
			}
		}
	}
	return info
}

func braceBlock(s string) (string, bool) {
	depth := 0
	start := -1
	for i, r := range s {
		switch r {
		case '{':
			if depth == 0 {
				start = i + 1
			}
			depth++
		case '}':
			depth--
			if depth == 0 {
				return s[start:i], true
			}
		}
	}
	return "", false
}

// Root where most local (non well-known) imports resolve as real files and
// which is still an ancestor of every proto — so `protoc -I <root>` matches
// package-relative imports (acme/v1/x.proto). Falls back to common ancestor.
func resolveProtoRoot(fileAbs []string, imports []string, fallback string) string {
	roots := map[string]int{}
	for _, imp := range imports {
		if strings.HasPrefix(imp, "google/") {
			continue // well-known / externally provided
		}
		suffix := string(filepath.Separator) + filepath.FromSlash(imp)
		for _, f := range fileAbs {
			if strings.HasSuffix(f, suffix) {
				roots[f[:len(f)-len(suffix)]]++
			}
		}
	}
	best, bestCount := "", 0
	for r, cnt := range roots {
		if !isAncestorOfAll(r, fileAbs) {
			continue
		}
		if cnt > bestCount || (cnt == bestCount && len(r) < len(best)) {
			best, bestCount = r, cnt
		}
	}
	if best == "" {
		return fallback
	}
	return best
}

func isAncestorOfAll(root string, files []string) bool {
	prefix := root + string(filepath.Separator)
	for _, f := range files {
		if !strings.HasPrefix(f, prefix) {
			return false
		}
	}
	return true
}

func commonDir(dirs []string) string {
	if len(dirs) == 0 {
		return ""
	}
	parts := strings.Split(filepath.Clean(dirs[0]), string(filepath.Separator))
	for _, d := range dirs[1:] {
		dp := strings.Split(filepath.Clean(d), string(filepath.Separator))
		n := 0
		for n < len(parts) && n < len(dp) && parts[n] == dp[n] {
			n++
		}
		parts = parts[:n]
	}
	if len(parts) == 0 {
		return string(filepath.Separator)
	}
	return strings.Join(parts, string(filepath.Separator))
}
