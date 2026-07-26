package cli

import (
	"os"
	"path/filepath"
	"testing"
)

const specOpenAPI = `openapi: 3.0.3
info:
  title: Billing API
  version: 1.0.0
servers:
  - url: https://api.acme.com
paths:
  /users:
    get:
      operationId: listUsers
      responses:
        "200": { description: ok }
`

func repoWith(t *testing.T, rel, content string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// The exit codes are a published contract; scripts branch on them, so each one
// needs a case that actually runs the binary's entry point.
func TestRunExitCodes(t *testing.T) {
	usable := repoWith(t, "api/openapi.yaml", specOpenAPI)
	empty := t.TempDir()

	// Exit 3 (write failure): --out points at a file, not a directory.
	notADir := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(notADir, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		args []string
		want int
	}{
		{"usable source", []string{usable, "--out", filepath.Join(t.TempDir(), "o")}, exitOK},
		{"missing --out", []string{usable}, exitUsage},
		{"no inputs", []string{"--out", filepath.Join(t.TempDir(), "o")}, exitUsage},
		{"unknown flag", []string{usable, "--out", filepath.Join(t.TempDir(), "o"), "--nope"}, exitUsage},
		{"bad --prefer", []string{usable, "--out", filepath.Join(t.TempDir(), "o"), "--prefer", "grpc"}, exitUsage},
		{"nothing usable", []string{empty, "--out", filepath.Join(t.TempDir(), "o")}, exitNoSources},
		{"--out is a file", []string{usable, "--out", notADir}, exitWrite},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Run(c.args); got != c.want {
				t.Errorf("Run(%v) = %d, want %d", c.args, got, c.want)
			}
		})
	}
}

// --version must work without the otherwise-required input and --out.
func TestRunVersion(t *testing.T) {
	if got := Run([]string{"--version"}); got != exitOK {
		t.Errorf("Run(--version) = %d, want %d", got, exitOK)
	}
}

// --out pointing at a file is one failure, so it must report one exit code.
// Reaching it through --merge used to surface a different error from a different
// code path and land on "usage error" instead of "write failure".
func TestOutIsFileExitsWriteFailureWithAndWithoutMerge(t *testing.T) {
	dir := t.TempDir()
	notADir := filepath.Join(dir, "out")
	if err := os.WriteFile(notADir, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	in := filepath.Join(dir, "in")
	if err := os.MkdirAll(in, 0o755); err != nil {
		t.Fatal(err)
	}

	for _, args := range [][]string{
		{in, "--out", notADir},
		{in, "--out", notADir, "--merge"},
	} {
		if got := Run(args); got != exitWrite {
			t.Errorf("Run(%v) = %d, want %d (write failure)", args, got, exitWrite)
		}
	}
}
