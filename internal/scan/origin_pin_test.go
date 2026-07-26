package scan

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestSanitizeRemoteURL(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		// The token form CI actually produces.
		{"https token", "https://x-access-token:ghp_secret@github.com/o/r.git", "https://github.com/o/r.git"},
		{"https user only", "https://user@github.com/o/r.git", "https://github.com/o/r.git"},
		// Over SSH the username is part of the address, not a credential: dropping
		// it yields a URL nobody can clone.
		{"ssh keeps user", "ssh://git@github.com/o/r.git", "ssh://git@github.com/o/r.git"},
		{"ssh drops password", "ssh://git:pw@host/r.git", "ssh://git@host/r.git"},
		// scp form carries no password and url.Parse would mangle it.
		{"scp form untouched", "git@github.com:o/r.git", "git@github.com:o/r.git"},
		{"plain https untouched", "https://github.com/o/r.git", "https://github.com/o/r.git"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := sanitizeRemoteURL(c.in); got != c.want {
				t.Errorf("sanitizeRemoteURL(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func gitInit(t *testing.T, dir string, args ...[]string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	base := [][]string{
		{"init", "-q"},
		{"config", "user.email", "t@example.com"},
		{"config", "user.name", "t"},
		{"config", "commit.gpgsign", "false"},
	}
	for _, a := range append(base, args...) {
		cmd := exec.Command("git", a...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", a, err, out)
		}
	}
}

// A repo_url + pinned_tag origin claims the files can be fetched from that ref.
// An untracked file cannot, so the honest answer is local_path plus a copy of
// the bytes actually scanned — otherwise `lathe sync-specs` chases a path that
// is not in the repository at that commit.
func TestUntrackedSpecFallsBackToLocalPath(t *testing.T) {
	in := t.TempDir()
	gitInit(t, in)
	writeFile(t, in, "README.md", "seed\n")
	gitInit(t, in, []string{"add", "."}, []string{"commit", "-qm", "seed"},
		[]string{"remote", "add", "origin", "https://example.com/o/r.git"})
	// Written after the commit: present on disk, absent from the ref.
	writeFile(t, in, "openapi.yaml", specOpenAPI)

	out := t.TempDir()
	if err := Execute(Options{Inputs: []string{in}, Out: out}); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	s := firstSource(t, filepath.Join(out, sourcesFileName))
	if s["repo_url"] != nil {
		t.Errorf("untracked spec must not claim a repo_url origin: %v", s)
	}
	if s["local_path"] == nil {
		t.Errorf("expected local_path fallback, got %v", s)
	}
	rep := readReport(t, out).Sources[0]
	if !hasGap(rep.Gaps, gapOriginNotAtRef, false) {
		t.Errorf("fallback must be explained by a gap, got %+v", rep.Gaps)
	}
	if _, err := os.Stat(filepath.Join(out, rep.Name, "openapi.yaml")); err != nil {
		t.Errorf("local_path source did not copy its spec: %v", err)
	}
}

// The nastier case: the file is tracked, so it looks pinnable, but the working
// copy differs from the ref. Emitting repo_url here silently swaps the spec for
// a different one at sync time.
func TestModifiedSpecFallsBackToLocalPath(t *testing.T) {
	in := t.TempDir()
	gitInit(t, in)
	writeFile(t, in, "openapi.yaml", specOpenAPI)
	gitInit(t, in, []string{"add", "."}, []string{"commit", "-qm", "spec"},
		[]string{"remote", "add", "origin", "https://example.com/o/r.git"})
	writeFile(t, in, "openapi.yaml", strings.Replace(specOpenAPI, "Billing API", "Billing API Edited", 1))

	out := t.TempDir()
	if err := Execute(Options{Inputs: []string{in}, Out: out}); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if s := firstSource(t, filepath.Join(out, sourcesFileName)); s["repo_url"] != nil {
		t.Errorf("locally modified spec must not claim a repo_url origin: %v", s)
	}
}

// The positive case must keep working, and the credentials in the remote must
// not reach any of the three artifacts.
func TestCleanTrackedSpecPinsAndRedactsCredentials(t *testing.T) {
	in := t.TempDir()
	gitInit(t, in)
	writeFile(t, in, "openapi.yaml", specOpenAPI)
	gitInit(t, in, []string{"add", "."}, []string{"commit", "-qm", "spec"},
		[]string{"remote", "add", "origin", "https://x-access-token:ghp_supersecret@example.com/o/r.git"})

	out := t.TempDir()
	if err := Execute(Options{Inputs: []string{in}, Out: out}); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	s := firstSource(t, filepath.Join(out, sourcesFileName))
	if s["repo_url"] == nil {
		t.Errorf("a clean tracked spec should pin to repo_url, got %v", s)
	}
	for _, name := range []string{sourcesFileName, reportFileName, gapsFileName} {
		data, err := os.ReadFile(filepath.Join(out, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if strings.Contains(string(data), "ghp_supersecret") {
			t.Errorf("%s leaked the remote credential:\n%s", name, data)
		}
	}
}
