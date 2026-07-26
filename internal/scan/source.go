package scan

import (
	"fmt"
	"path/filepath"
	"strings"
)

type builtSource struct {
	Name      string
	baseName  string
	fromInput string // input as the user spelled it, for the report
	inputKey  string // absolute input path; identity across runs, see provenance
	// identity locates this source inside its input (spec path, collection file).
	// It must not depend on what else the scan found: a key that shifts as the
	// corpus grows re-points a name at a different API and --merge drops the policy
	// hanging off the old entry. Backends yielding at most one source per input
	// (proto, graphql, L2) use a constant — anything derived from the files found
	// moves the moment a file is added, and provKey already includes the backend.
	identity string

	origin *Origin
	yc     *ycSource
	copies []copyItem
	synth  []synthFile

	report *SourceReport
}

// provenance lets --merge recognize an entry this tool wrote earlier, so a
// rescan updates it instead of appending a copy. Deliberately independent of the
// assigned name, which --name and collision suffixes can change.
func (b *builtSource) provenance() *Provenance {
	return &Provenance{
		Input:   b.inputKey,
		Backend: b.yc.Backend,
		Key:     b.identity,
	}
}

type copyItem struct {
	absFrom string
	relTo   string
}

type synthFile struct {
	relTo   string
	content []byte
}

func (b *builtSource) sortKey() string {
	first := ""
	if len(b.copies) > 0 {
		first = b.copies[0].absFrom
	}
	return strings.Join([]string{b.baseName, b.yc.Backend, b.fromInput, first}, "\x00")
}

func buildSource(c *Candidate, p *parsed, root string, git *gitOrigin) *builtSource {
	// Lathe keeps external file $refs raw, so multi-file specs must be bundled
	// into one self-contained file. Bundled artifacts are synthesized and always
	// local_path (origin pinning is superseded).
	if p.hasExtRefs {
		if b := bundleSource(c, p, root); b != nil {
			return b
		}
	}

	repoName := ""
	if git != nil {
		repoName = git.repoName
	}
	b := &builtSource{
		baseName: firstNonEmpty(sanitizeName(p.title), sanitizeName(repoName), sanitizeName(parentDirName(c.Path)), "source"),
		identity: c.Path,
		yc:       &ycSource{DefaultHostname: p.hostname, Backend: p.format},
	}

	block := &filesBlock{Files: []string{c.Path}}
	pinned := git.pinnable(block.Files)
	if pinned {
		b.origin = &Origin{Type: "repo_url", RepoURL: git.repoURL, PinnedTag: git.pinnedTag, RefKind: git.refKind}
		b.yc.RepoURL = git.repoURL
		b.yc.PinnedTag = git.pinnedTag
	} else {
		b.origin = &Origin{Type: "local_path"}
		base := filepath.Base(c.Path)
		block.Files = []string{base}
		b.copies = []copyItem{{absFrom: filepath.Join(root, filepath.FromSlash(c.Path)), relTo: base}}
	}
	switch p.format {
	case "openapi3":
		b.yc.OpenAPI3 = block
	case "swagger":
		b.yc.Swagger = block
	}

	sr := &SourceReport{
		Backend: p.format, Level: "L1",
		WouldEmitCommands: p.wouldEmit,
		DefaultHostname:   p.hostname,
		Metrics:           c.Metrics,
		Files:             block.Files,
	}
	var gaps []Gap
	if p.wouldEmit == 0 {
		gaps = append(gaps, Gap{Kind: gapParseError, Scope: "source",
			Message: "spec parses but declares no operations; Lathe would emit zero commands", Blocking: true})
	}
	if p.hasExtRefs {
		gaps = append(gaps, Gap{Kind: gapRefUnresolved, Scope: "source",
			Message:  "spec uses external $ref that could not be bundled (missing or unsupported target); resolve manually",
			Blocking: true})
	}
	if p.hostname == "" {
		gaps = append(gaps, Gap{Kind: gapAmbiguousHost, Scope: "source", Message: hostGapMessage(p), Blocking: false})
	}
	if g, ok := notAtRefGap(git, pinned); ok {
		gaps = append(gaps, g)
	}
	sr.Gaps = gaps
	sr.Confidence = confidenceFor(p.wouldEmit, gaps)
	b.report = sr
	return b
}

// hostGapMessage distinguishes "the spec named no server" from "the spec named
// several and scan will not pick for you"; the second needs the candidates, or
// the user cannot act on it without reopening the spec.
func hostGapMessage(p *parsed) string {
	if len(p.hostCandidates) > 1 {
		return fmt.Sprintf("servers declare %d different hostnames (%s); set default_hostname to the one you mean before relying on auth",
			len(p.hostCandidates), strings.Join(p.hostCandidates, ", "))
	}
	return "no server hostname detected; set default_hostname before relying on auth"
}

// notAtRefGap explains a fallback that would otherwise look arbitrary: the input
// is a pinnable repository, yet this source shipped as copied files. Saying so
// is the difference between "scan chose local_path" and "these files are not in
// your repository at that commit", which is usually news to the user.
func notAtRefGap(git *gitOrigin, pinned bool) (Gap, bool) {
	if git == nil || pinned {
		return Gap{}, false
	}
	return Gap{Kind: gapOriginNotAtRef, Scope: "source",
		Message: "source files are untracked, ignored, or locally modified, so they are not retrievable from " +
			git.repoURL + " at " + git.pinnedTag + "; emitted local_path with copies of the scanned bytes instead",
		Blocking: false}, true
}

// bundleSource returns nil when the closure cannot be fully resolved so
// buildSource can flag a blocking gap.
func bundleSource(c *Candidate, p *parsed, root string) *builtSource {
	primaryAbs := filepath.Join(root, filepath.FromSlash(c.Path))
	bundled, files, missing, err := bundleSpec(primaryAbs, p.format, root)
	if err != nil || len(missing) > 0 {
		return nil
	}

	draftName := "openapi.yaml"
	if p.format == "swagger" {
		draftName = "swagger.yaml"
	}
	block := &filesBlock{Files: []string{draftName}}
	b := &builtSource{
		baseName: firstNonEmpty(sanitizeName(p.title), sanitizeName(parentDirName(c.Path)), "api"),
		identity: c.Path,
		origin:   &Origin{Type: "local_path"},
		yc:       &ycSource{DefaultHostname: p.hostname, Backend: p.format},
		synth:    []synthFile{{relTo: draftName, content: bundled}},
	}
	if p.format == "swagger" {
		b.yc.Swagger = block
	} else {
		b.yc.OpenAPI3 = block
	}

	gaps := []Gap{{Kind: gapRefBundled, Scope: "source",
		Message:  fmt.Sprintf("bundled from %d files into one self-contained spec; verify the merged result", len(files)),
		Blocking: false}}
	if p.hostname == "" {
		gaps = append(gaps, Gap{Kind: gapAmbiguousHost, Scope: "source", Message: hostGapMessage(p), Blocking: false})
	}
	b.report = &SourceReport{
		Backend: p.format, Level: "L1",
		WouldEmitCommands: p.wouldEmit,
		DefaultHostname:   p.hostname,
		Metrics:           c.Metrics,
		Files:             block.Files,
		Gaps:              gaps,
		Confidence:        confidenceFor(p.wouldEmit, gaps),
	}
	return b
}

func confidenceFor(wouldEmit int, gaps []Gap) string {
	if hasBlocking(gaps) {
		return confLow
	}
	if wouldEmit > 0 {
		return confHigh
	}
	return confLow
}

func hasBlocking(gaps []Gap) bool {
	for _, g := range gaps {
		if g.Blocking {
			return true
		}
	}
	return false
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func parentDirName(relPath string) string {
	return strings.TrimSuffix(filepath.Base(relPath), filepath.Ext(relPath))
}
