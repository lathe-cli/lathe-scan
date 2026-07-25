package scan

import (
	"context"
	"os/exec"
	"strings"
	"time"
)

type gitOrigin struct {
	root      string
	repoURL   string
	pinnedTag string // immutable tag or 40-char SHA
	refKind   string // tag|sha
	repoName  string
}

// Offline only (local git state). Nil when not a worktree, no remote, or no
// immutable ref — callers fall back to local_path.
func detectGitOrigin(dir string) *gitOrigin {
	root := git(dir, "rev-parse", "--show-toplevel")
	if root == "" {
		return nil
	}
	remote := git(dir, "config", "--get", "remote.origin.url")
	if remote == "" {
		return nil
	}

	o := &gitOrigin{root: root, repoURL: remote, repoName: repoNameFromURL(remote)}

	// Tag at HEAD, else full SHA. Both are immutable and accepted by Lathe's
	// validateRef; floating refs never are.
	if tag := firstLine(git(dir, "tag", "--points-at", "HEAD")); tag != "" {
		o.pinnedTag, o.refKind = tag, "tag"
	} else if sha := git(dir, "rev-parse", "HEAD"); len(sha) == 40 && isHex(sha) {
		o.pinnedTag, o.refKind = sha, "sha"
	} else {
		return nil
	}
	return o
}

// isGitWorktree distinguishes "not a repo" (local_path is the only honest
// origin) from "a repo we could not pin" (worth reporting as a gap).
func isGitWorktree(dir string) bool {
	return git(dir, "rev-parse", "--show-toplevel") != ""
}

func git(dir string, args ...string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return strings.TrimSpace(s)
}

func repoNameFromURL(u string) string {
	u = strings.TrimSpace(u)
	u = strings.TrimSuffix(u, ".git")
	u = strings.TrimRight(u, "/")
	if i := strings.LastIndexAny(u, "/:"); i >= 0 {
		u = u[i+1:]
	}
	return u
}

func isHex(s string) bool {
	for _, c := range s {
		switch {
		case c >= '0' && c <= '9', c >= 'a' && c <= 'f':
		default:
			return false
		}
	}
	return true
}
