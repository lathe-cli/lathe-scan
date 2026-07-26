package scan

import (
	"context"
	"net/url"
	"os/exec"
	"path/filepath"
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

	o := &gitOrigin{root: root, repoURL: sanitizeRemoteURL(remote), repoName: repoNameFromURL(remote)}

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

// pinnable reports whether every file in a source's closure can actually be
// fetched from repo_url at pinned_tag with the bytes this scan just read.
//
// Being inside a pinned repository is not the same claim: an untracked, ignored,
// or locally modified file is not at that ref, and a manifest that says
// otherwise sends `lathe sync-specs` after something absent — or worse, after
// different content under the same path, which no one notices. Ownership of that
// distinction belongs here rather than at each backend, because the honest
// fallback (local_path plus a copy of the bytes we saw) is identical for all of
// them.
//
// relFiles are repo-root-relative, as written into the manifest. Empty means
// nothing to verify, which cannot be an honest pin either.
func (o *gitOrigin) pinnable(relFiles []string) bool {
	if o == nil || len(relFiles) == 0 {
		return false
	}
	args := append([]string{"status", "--porcelain=v1", "-z",
		"--untracked-files=all", "--ignored=matching", "--"}, relFiles...)
	// Any reported entry means dirty, untracked, or ignored — all disqualifying.
	// An error (not a worktree, bad path) is also a refusal: this must fail closed.
	out, err := gitRaw(o.root, args...)
	if err != nil || strings.Trim(out, "\x00") != "" {
		return false
	}

	// status stays silent about a path git has never heard of only when it is
	// also ignored, so confirm each file is actually tracked.
	out, err = gitRaw(o.root, append([]string{"ls-files", "-z", "--"}, relFiles...)...)
	if err != nil {
		return false
	}
	tracked := map[string]bool{}
	for _, f := range strings.Split(out, "\x00") {
		if f != "" {
			tracked[f] = true
		}
	}
	for _, f := range relFiles {
		if !tracked[filepath.ToSlash(f)] {
			return false
		}
	}
	return true
}

// gitRaw is git() without the trimming, for -z output where empty vs non-empty
// is the answer and NUL is the separator.
func gitRaw(dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
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

// sanitizeRemoteURL strips credentials from a remote before it becomes
// provenance. An origin has to say which repository this came from; it never
// needs the authority to fetch it. CI clones routinely carry a token in the URL
// (https://x-access-token:<token>@github.com/...), and repo_url is written into
// sources.yaml, report.json and GAPS.md — files whose whole purpose is to be
// committed and shared.
//
// Username handling differs by scheme because its meaning does: over SSH the
// user is part of the identity (ssh://git@host/repo is not reachable without
// it), over HTTP it is only ever a credential slot. scp-form remotes
// (git@host:path) carry no password and would be mangled by url.Parse, so they
// are left alone.
func sanitizeRemoteURL(remote string) string {
	remote = strings.TrimSpace(remote)
	if !strings.Contains(remote, "://") {
		return remote // scp form or a local path
	}
	u, err := url.Parse(remote)
	if err != nil || u.User == nil {
		return remote
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
		u.User = nil
	default:
		u.User = url.User(u.User.Username())
	}
	return u.String()
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
