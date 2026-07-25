package scan

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Discovery respects .gitignore: a spec the repo itself declares as generated or
// vendored is not that repo's API contract, and selecting one is the dominant
// false positive after dependency trees.
//
// Supported: comments, blank lines, negation (!), directory-only (trailing /),
// anchoring (leading or interior /), and *, ?, **, [class] globs. Deeper files
// override shallower ones and the last matching pattern in a file wins, as in
// git. Not supported, and not reachable offline: core.excludesFile, and
// re-inclusion beneath an already-excluded directory (git forbids that too).

type ignorePattern struct {
	re      *regexp.Regexp
	negate  bool
	dirOnly bool
}

type ignoreLayer struct {
	base     string // absolute dir holding this .gitignore
	patterns []ignorePattern
}

// ignoreStack tracks the .gitignore files on the path to the directory being
// walked, outermost first.
type ignoreStack struct{ layers []ignoreLayer }

// seedParents pushes the .gitignore files between the repository root and
// rootDir, outermost first: scanning a subdirectory has to honor what its
// parents declare. Without a repository above rootDir there is nothing to inherit.
func (s *ignoreStack) seedParents(rootDir string) {
	// A rootDir that is itself a repository root inherits nothing: .gitignore
	// never applies across a repository boundary, and a checkout nested inside
	// another repo must not be blanked by the outer repo's rules.
	if _, err := os.Stat(filepath.Join(rootDir, ".git")); err == nil {
		return
	}
	var dirs []string
	for dir := filepath.Dir(rootDir); ; {
		dirs = append(dirs, dir)
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			break // repository root; git looks no further up
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return
		}
		dir = parent
	}
	for i := len(dirs) - 1; i >= 0; i-- {
		if l, ok := loadIgnoreFile(dirs[i]); ok {
			s.layers = append(s.layers, l)
		}
	}
}

// enter drops layers that are no longer ancestors of dir and pushes dir's own
// .gitignore. WalkDir's depth-first order makes the ancestor check sufficient.
func (s *ignoreStack) enter(dir string) {
	for n := len(s.layers); n > 0; n = len(s.layers) {
		if isAncestorDir(s.layers[n-1].base, dir) {
			break
		}
		s.layers = s.layers[:n-1]
	}
	if l, ok := loadIgnoreFile(dir); ok {
		s.layers = append(s.layers, l)
	}
}

func (s *ignoreStack) ignored(path string, isDir bool) bool {
	ignored := false
	for _, l := range s.layers {
		rel, err := filepath.Rel(l.base, path)
		if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
			continue
		}
		rel = filepath.ToSlash(rel)
		for _, p := range l.patterns {
			if p.dirOnly && !isDir {
				continue
			}
			if p.re.MatchString(rel) {
				ignored = !p.negate
			}
		}
	}
	return ignored
}

func loadIgnoreFile(dir string) (ignoreLayer, bool) {
	data, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if err != nil {
		return ignoreLayer{}, false
	}
	l := ignoreLayer{base: dir}
	for _, line := range strings.Split(string(data), "\n") {
		if p, ok := compileIgnorePattern(line); ok {
			l.patterns = append(l.patterns, p)
		}
	}
	if len(l.patterns) == 0 {
		return ignoreLayer{}, false
	}
	return l, true
}

func compileIgnorePattern(line string) (ignorePattern, bool) {
	line = strings.TrimRight(line, " \t\r")
	if line == "" || strings.HasPrefix(line, "#") {
		return ignorePattern{}, false
	}

	var p ignorePattern
	if strings.HasPrefix(line, "!") {
		p.negate = true
		line = line[1:]
	}
	line = strings.TrimPrefix(line, `\`) // escaped leading # or !
	if strings.HasSuffix(line, "/") {
		p.dirOnly = true
		line = strings.TrimSuffix(line, "/")
	}
	if line == "" {
		return ignorePattern{}, false
	}

	// A pattern with a slash left in it is anchored to the .gitignore's own
	// directory; a bare name matches at any depth.
	anchored := strings.HasPrefix(line, "/")
	line = strings.TrimPrefix(line, "/")
	if strings.Contains(line, "/") {
		anchored = true
	}

	re, err := regexp.Compile(ignoreRegexp(line, anchored))
	if err != nil {
		return ignorePattern{}, false
	}
	p.re = re
	return p, true
}

func ignoreRegexp(pat string, anchored bool) string {
	var b strings.Builder
	b.WriteString("^")
	if !anchored {
		b.WriteString(`(?:.*/)?`)
	}
	for i := 0; i < len(pat); {
		switch {
		case strings.HasPrefix(pat[i:], "**/"):
			b.WriteString(`(?:.*/)?`)
			i += 3
		case pat[i:] == "/**":
			b.WriteString(`(?:/.*)?`)
			i += 3
		case strings.HasPrefix(pat[i:], "**"):
			b.WriteString(`.*`)
			i += 2
		case pat[i] == '*':
			b.WriteString(`[^/]*`)
			i++
		case pat[i] == '?':
			b.WriteString(`[^/]`)
			i++
		case pat[i] == '[':
			class, next, ok := charClass(pat, i)
			if !ok {
				b.WriteString(regexp.QuoteMeta("["))
				i++
				continue
			}
			b.WriteString(class)
			i = next
		default:
			b.WriteString(regexp.QuoteMeta(pat[i : i+1]))
			i++
		}
	}
	// A matched directory takes its whole subtree with it.
	b.WriteString(`(?:/.*)?$`)
	return b.String()
}

// charClass copies a [...] glob class through to the regexp, translating git's
// [!...] negation into [^...]. Returns ok=false for an unterminated class.
func charClass(pat string, start int) (class string, next int, ok bool) {
	j := start + 1
	if j < len(pat) && (pat[j] == '!' || pat[j] == '^') {
		j++
	}
	if j < len(pat) && pat[j] == ']' {
		j++ // a literal ] as the first class member
	}
	for j < len(pat) && pat[j] != ']' {
		j++
	}
	if j >= len(pat) {
		return "", start, false
	}
	class = pat[start : j+1]
	if strings.HasPrefix(class, "[!") {
		class = "[^" + class[2:]
	}
	return class, j + 1, true
}

func isAncestorDir(base, dir string) bool {
	if base == dir {
		return true
	}
	return strings.HasPrefix(dir, base+string(filepath.Separator))
}
