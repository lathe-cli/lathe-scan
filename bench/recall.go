// Command bench measures discovery recall against a pinned corpus of real OSS
// repositories (bench/corpus.yaml). For each repository it makes a shallow
// checkout at the pinned commit, runs the built lathe-scan binary, and checks
// the report against the corpus expectations. Network access is required;
// this is a development benchmark, never part of `make check`.
//
// Run from the repository root after `make build`:
//
//	go run ./bench
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/lathe-cli/lathe-scan/internal/scan"
)

const (
	corpusPath = "bench/corpus.yaml"
	scanBinary = "bin/lathe-scan"
	cacheRoot  = ".local/bench/repos"
	outRoot    = ".local/bench/out"
)

type corpus struct {
	Repos []repoSpec `yaml:"repos"`
}

type repoSpec struct {
	Name     string   `yaml:"name"`
	URL      string   `yaml:"url"`
	Ref      string   `yaml:"ref"`
	KnownGap string   `yaml:"known_gap"`
	Expect   expected `yaml:"expect"`
}

type expected struct {
	MinUsable int      `yaml:"min_usable"`
	Backends  []string `yaml:"backends"`
	Extractor string   `yaml:"extractor"`
}

type result struct {
	repo      repoSpec
	usable    int
	backends  []string
	extractor string
	pass      bool
	note      string
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "bench:", err)
		os.Exit(1)
	}
}

func run() error {
	if _, err := os.Stat(scanBinary); err != nil {
		return fmt.Errorf("%s not found; run `make build` first", scanBinary)
	}
	data, err := os.ReadFile(corpusPath)
	if err != nil {
		return err
	}
	var c corpus
	if err := yaml.Unmarshal(data, &c); err != nil {
		return fmt.Errorf("parse %s: %w", corpusPath, err)
	}

	var results []result
	for _, repo := range c.Repos {
		fmt.Fprintf(os.Stderr, "==> %s\n", repo.Name)
		r := runOne(repo)
		results = append(results, r)
	}
	report(results)
	return nil
}

func runOne(repo repoSpec) result {
	r := result{repo: repo}
	dir, err := checkout(repo)
	if err != nil {
		r.note = "checkout: " + err.Error()
		return r
	}
	out := filepath.Join(outRoot, repo.Name)
	if err := os.RemoveAll(out); err != nil {
		r.note = err.Error()
		return r
	}

	// Exit 2 (nothing usable) is a valid benchmark outcome, not a harness error.
	cmd := exec.Command(scanBinary, dir, "--out", out)
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if !isExitCode(err, &exit, 2) {
			r.note = "scan: " + err.Error()
			return r
		}
	}

	rep, err := readReport(filepath.Join(out, "report.json"))
	if err != nil {
		r.note = err.Error()
		return r
	}
	r.usable = rep.Summary.Usable
	seen := map[string]bool{}
	for _, s := range rep.Sources {
		if !s.Recommended {
			continue
		}
		if !seen[s.Backend] {
			seen[s.Backend] = true
			r.backends = append(r.backends, s.Backend)
		}
		if s.Extractor != "" && r.extractor == "" {
			r.extractor = s.Extractor
		}
	}
	r.pass, r.note = score(repo.Expect, r)
	return r
}

func score(want expected, got result) (bool, string) {
	if got.usable < want.MinUsable {
		return false, fmt.Sprintf("usable %d < %d", got.usable, want.MinUsable)
	}
	for _, b := range want.Backends {
		if !contains(got.backends, b) {
			return false, fmt.Sprintf("backend %s missing (got %v)", b, got.backends)
		}
	}
	if want.Extractor != "" && got.extractor != want.Extractor {
		return false, fmt.Sprintf("extractor %q, want %q", got.extractor, want.Extractor)
	}
	return true, ""
}

// checkout fetches exactly the pinned commit, shallow. The cache is keyed by
// the checked-out commit, so a pin update invalidates it naturally.
func checkout(repo repoSpec) (string, error) {
	dir := filepath.Join(cacheRoot, repo.Name)
	if sha, err := gitOut(dir, "rev-parse", "HEAD"); err == nil && sha == repo.Ref {
		return dir, nil
	}
	if err := os.RemoveAll(dir); err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	steps := [][]string{
		{"init", "--quiet"},
		{"remote", "add", "origin", repo.URL},
		{"fetch", "--quiet", "--depth", "1", "origin", repo.Ref},
		{"checkout", "--quiet", "FETCH_HEAD"},
	}
	for _, args := range steps {
		if _, err := gitOut(dir, args...); err != nil {
			return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
		}
	}
	return dir, nil
}

func gitOut(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

func readReport(path string) (*scan.Report, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var r scan.Report
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return &r, nil
}

func report(results []result) {
	fmt.Printf("\n%-24s %-6s %-8s %-22s %s\n", "REPO", "PASS", "USABLE", "BACKEND/EXTRACTOR", "NOTE")
	scored, passed := 0, 0
	for _, r := range results {
		status := "FAIL"
		note := r.note
		if r.repo.KnownGap != "" {
			status = "gap"
			note = r.repo.KnownGap
			// A known gap that starts passing must be promoted to a scored entry.
			if r.pass && r.usable > 0 {
				note = "KNOWN GAP NOW PASSES — promote to scored: " + note
			}
		} else {
			scored++
			if r.pass {
				status = "PASS"
				passed++
			}
		}
		kind := strings.Join(r.backends, ",")
		if r.extractor != "" {
			kind += "/" + r.extractor
		}
		fmt.Printf("%-24s %-6s %-8d %-22s %s\n", r.repo.Name, status, r.usable, kind, note)
	}
	fmt.Printf("\nrecall: %d/%d", passed, scored)
	if scored > 0 {
		fmt.Printf(" (%.0f%%)", float64(passed)/float64(scored)*100)
	}
	fmt.Println()
	if passed < scored {
		os.Exit(1)
	}
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func isExitCode(err error, exit **exec.ExitError, code int) bool {
	if e, ok := err.(*exec.ExitError); ok {
		*exit = e
		return e.ExitCode() == code
	}
	return false
}
