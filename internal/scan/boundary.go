package scan

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// The scanner reads only what the user pointed it at. A symlink inside an input
// tree can name any file on the machine, so a lexical path check answers the
// wrong question: not "does this string look like it is under the root" but
// "where will open() actually land". Every read entry point resolves the
// physical path first and refuses anything outside the input root — otherwise a
// hostile repository turns a scan into an exfiltration primitive, since whatever
// is read can be copied into --out and committed alongside sources.yaml.

// resolveWithin resolves path through symlinks and reports whether the result
// stays inside root. Both sides are resolved: comparing a physical path against
// a logical root is how a scan of /tmp/x on macOS (where /tmp is itself a
// symlink) would refuse every file it just found.
// A path that cannot be resolved (missing, dangling symlink, permission) is
// refused: callers turn that into a recorded candidate error or a blocking gap,
// never a silent skip.
func resolveWithin(root, path string) (string, bool) {
	phys, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", false
	}
	physRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", false
	}
	return phys, pathUnderRoot(physRoot, phys)
}

// physicalRoot resolves the input root once so every later comparison is
// physical-vs-physical. It also keeps the root consistent with `git rev-parse
// --show-toplevel`, which always reports a physical path: without this, an input
// reached through a symlink (macOS /tmp, for one) makes every repo-relative path
// fall back to a bare basename.
func physicalRoot(dir string) (string, error) {
	return filepath.EvalSymlinks(dir)
}

// readWithin is readCapped plus the boundary check. Read sites that already hold
// a physical root use it instead of reading paths straight from the walk.
func readWithin(root, path string) ([]byte, error) {
	phys, ok := resolveWithin(root, path)
	if !ok {
		return nil, fmt.Errorf("%s resolves outside the input root", path)
	}
	return readCapped(phys)
}

// pathWithin distinguishes a boundary refusal from an ordinary read failure, so
// callers can report the two differently.
func pathWithin(root, path string) bool {
	_, ok := resolveWithin(root, path)
	return ok
}

// containsSymlink reports the first existing component from base down to path
// that is a symlink. Writing through one escapes --out just as reading through
// one escapes the input root, and the final path being clean is not enough:
// out/pkg/schemas -> /elsewhere makes MkdirAll succeed and WriteFile land
// outside. Components that do not exist yet are safe — they are about to be
// created as real directories.
func containsSymlink(base, path string) (string, error) {
	rel, err := filepath.Rel(base, path)
	if err != nil {
		return "", err
	}
	cur := base
	if info, err := os.Lstat(cur); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return cur, nil
	}
	for _, seg := range strings.Split(filepath.ToSlash(rel), "/") {
		if seg == "" || seg == "." {
			continue
		}
		cur = filepath.Join(cur, seg)
		info, err := os.Lstat(cur)
		if err != nil {
			// Does not exist yet; nothing below it can exist either.
			return "", nil
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return cur, nil
		}
	}
	return "", nil
}
