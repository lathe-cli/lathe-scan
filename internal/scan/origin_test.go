package scan

import (
	"os/exec"
	"testing"
)

func TestRepoNameFromURL(t *testing.T) {
	cases := map[string]string{
		"https://github.com/acme/billing.git": "billing",
		"git@github.com:acme/billing.git":     "billing",
		"https://example.com/x/y/":            "y",
		"ssh://git@host/team/service":         "service",
	}
	for in, want := range cases {
		if got := repoNameFromURL(in); got != want {
			t.Errorf("repoNameFromURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestIsHex(t *testing.T) {
	if !isHex("0123456789abcdef") {
		t.Error("valid hex reported non-hex")
	}
	if isHex("XYZ") || isHex("0A") { // uppercase is not a git lowercase SHA
		t.Error("invalid hex reported as hex")
	}
}

func TestDetectGitOrigin(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	run("config", "user.email", "t@example.com")
	run("config", "user.name", "t")
	run("config", "commit.gpgsign", "false")
	run("remote", "add", "origin", "https://github.com/acme/billing.git")
	writeFile(t, dir, "openapi.yaml", specOpenAPI)
	run("add", "-A")
	run("commit", "-q", "-m", "init")

	// No tag yet: origin should pin the 40-char HEAD SHA.
	o := detectGitOrigin(dir)
	if o == nil {
		t.Fatal("detectGitOrigin returned nil for a git repo with a remote")
	}
	if o.repoURL != "https://github.com/acme/billing.git" {
		t.Errorf("repoURL = %q", o.repoURL)
	}
	if o.refKind != "sha" || len(o.pinnedTag) != 40 || !isHex(o.pinnedTag) {
		t.Errorf("expected 40-char SHA, got kind=%q tag=%q", o.refKind, o.pinnedTag)
	}
	if o.repoName != "billing" {
		t.Errorf("repoName = %q, want billing", o.repoName)
	}

	// With a tag on HEAD: origin should prefer the immutable tag.
	run("tag", "v1.2.3")
	o = detectGitOrigin(dir)
	if o.refKind != "tag" || o.pinnedTag != "v1.2.3" {
		t.Errorf("expected tag v1.2.3, got kind=%q tag=%q", o.refKind, o.pinnedTag)
	}
}

func TestDetectGitOriginNonRepo(t *testing.T) {
	if detectGitOrigin(t.TempDir()) != nil {
		t.Error("non-git dir should yield nil origin")
	}
}
