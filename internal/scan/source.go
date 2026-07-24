package scan

import (
	"path/filepath"
	"strings"
)

// buildSource turns one usable candidate into a resolved source (origin, files,
// gaps, confidence). Name is assigned later during collision resolution.
func buildSource(c *Candidate, p *parsed, root, inputAbs string, git *gitOrigin, opts Options) *builtSource {
	_ = inputAbs // reserved for future per-input relative logic

	repoName := ""
	if git != nil {
		repoName = git.repoName
	}
	b := &builtSource{
		backend:  p.format,
		hostname: p.hostname,
		baseName: firstNonEmpty(sanitizeName(p.title), sanitizeName(repoName), sanitizeName(parentDirName(c.Path)), "source"),
	}

	sr := &SourceReport{
		Backend:           p.format,
		Level:             "L1",
		WouldEmitCommands: p.wouldEmit,
		DefaultHostname:   p.hostname,
		Metrics:           c.Metrics,
	}

	if git != nil {
		b.origin = &Origin{Type: "repo_url", RepoURL: git.repoURL, PinnedTag: git.pinnedTag, RefKind: git.refKind}
		b.files = []string{c.Path} // path is relative to the repo root
	} else {
		b.origin = &Origin{Type: "local_path"} // LocalPath set to the source subdir at write time
		b.copyFrom = filepath.Join(root, filepath.FromSlash(c.Path))
		b.copyTo = filepath.Base(c.Path)
		b.files = []string{b.copyTo}
	}
	sr.Origin = b.origin
	sr.Files = b.files

	var gaps []Gap
	if p.wouldEmit == 0 {
		gaps = append(gaps, Gap{
			Kind: gapParseError, Scope: "source",
			Message:  "spec parses but declares no operations; Lathe would emit zero commands",
			Blocking: true,
		})
	}
	if p.hasExtRefs {
		gaps = append(gaps, Gap{
			Kind: gapRefUnresolved, Scope: "source",
			Message:  "spec uses external $ref; reference closure is not yet resolved",
			Blocking: git == nil, // a single-file local copy breaks external refs
		})
	}
	if p.hostname == "" {
		gaps = append(gaps, Gap{
			Kind: gapAmbiguousHost, Scope: "source",
			Message:  "no server hostname detected; set default_hostname before relying on auth",
			Blocking: false,
		})
	}
	sr.Gaps = gaps
	sr.Confidence = confidenceFor(p, gaps)

	b.report = sr
	return b
}

func confidenceFor(p *parsed, gaps []Gap) string {
	for _, g := range gaps {
		if g.Blocking {
			return confLow
		}
	}
	if p.wouldEmit > 0 {
		return confHigh
	}
	return confLow
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
	dir := filepath.Dir(filepath.FromSlash(relPath))
	base := filepath.Base(dir)
	if base == "." || base == string(filepath.Separator) {
		return ""
	}
	return strings.TrimSuffix(filepath.Base(relPath), filepath.Ext(relPath))
}
