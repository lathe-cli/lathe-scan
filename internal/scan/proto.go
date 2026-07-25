package scan

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
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
	infos := make([]protoFileInfo, 0, len(files))
	dirs := make([]string, 0, len(files))
	var methods, httpMethods, services int
	var imports []string
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil || len(data) > maxSpecBytes {
			continue
		}
		info := analyzeProto(relOrBase(root, f), data)
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
	protoDir := resolveProtoRoot(files, imports, commonDir(dirs))
	cand := &Candidate{
		Path: relOrBase(root, protoDir), Format: "proto", Parsed: true,
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

	var entries []string
	for _, info := range infos {
		if info.services > 0 {
			if rel, err := filepath.Rel(protoDir, filepath.Join(root, filepath.FromSlash(info.rel))); err == nil {
				entries = append(entries, filepath.ToSlash(rel))
			}
		}
	}
	// No services: still stage every file so protoc has entrypoints.
	if len(entries) == 0 {
		for _, f := range files {
			if rel, err := filepath.Rel(protoDir, f); err == nil {
				entries = append(entries, filepath.ToSlash(rel))
			}
		}
	}

	repoName := ""
	if git != nil {
		repoName = git.repoName
	}
	b := &builtSource{
		baseName: firstNonEmpty(sanitizeName(repoName), sanitizeName(filepath.Base(protoDir)), "proto"),
		identity: "proto",
		yc:       &ycSource{Backend: "proto"},
	}
	block := &protoBlock{Entries: entries}
	if git != nil {
		b.origin = &Origin{Type: "repo_url", RepoURL: git.repoURL, PinnedTag: git.pinnedTag, RefKind: git.refKind}
		b.yc.RepoURL = git.repoURL
		b.yc.PinnedTag = git.pinnedTag
		from := "."
		if rel, err := filepath.Rel(git.root, protoDir); err == nil && rel != "." {
			from = filepath.ToSlash(rel)
		}
		block.Staging = []stagingEntry{{From: from, To: "."}}
	} else {
		b.origin = &Origin{Type: "local_path"}
		block.Staging = []stagingEntry{{From: ".", To: "."}}
		for _, f := range files {
			if rel, err := filepath.Rel(protoDir, f); err == nil {
				b.copies = append(b.copies, copyItem{absFrom: f, relTo: filepath.ToSlash(rel)})
			}
		}
	}
	b.yc.Proto = block

	b.report = &SourceReport{
		Level: "L1", Backend: "proto",
		WouldEmitCommands: httpMethods,
		Files:             entries,
		Metrics:           &Metrics{Operations: methods},
		Gaps: []Gap{{Kind: gapProtoImports, Scope: "source",
			Message: "staging and import roots were inferred statically; run `lathe sync-specs` (needs protoc) to verify the tree compiles", Blocking: false}},
		Confidence: confMedium,
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
