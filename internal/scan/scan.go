// Package scan discovers API specs and emits a draft Lathe sources.yaml.
//
// It never imports Lathe internals; it mirrors Lathe's lenient parse and
// command-emit rules so "usable" here implies Lathe will load and generate.
package scan

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type Options struct {
	Inputs []string
	Out    string
	Name   string
	Prefer string
	Merge  bool
	Force  bool
	JSON   bool
}

// ErrNoSources maps to exit code 2.
type ErrNoSources struct{ msg string }

func (e ErrNoSources) Error() string { return e.msg }

// ErrWrite maps to exit code 3.
type ErrWrite struct{ err error }

func (e ErrWrite) Error() string { return "write output: " + e.err.Error() }
func (e ErrWrite) Unwrap() error { return e.err }

var preferBackends = map[string]bool{
	"openapi3": true, "swagger": true, "proto": true, "graphql": true,
}

func Execute(opts Options) error {
	if strings.TrimSpace(opts.Out) == "" {
		return fmt.Errorf("--out is required")
	}
	if opts.Name != "" && len(opts.Inputs) != 1 {
		return fmt.Errorf("--name is only valid with a single input")
	}
	if opts.Prefer != "" && !preferBackends[opts.Prefer] {
		return fmt.Errorf("--prefer %q: want one of openapi3, swagger, proto, graphql", opts.Prefer)
	}
	// Checked before anything is read so --out being a file reports the same way
	// whether or not --merge made us touch it first.
	if info, err := os.Stat(opts.Out); err == nil && !info.IsDir() {
		return ErrWrite{err: fmt.Errorf("--out is not a directory: %s", opts.Out)}
	}

	// Non-nil so report.json carries [] rather than null: an empty list is a
	// cleaner contract for anything consuming this file.
	report := &Report{SchemaVersion: 3, ToolVersion: version, Gaps: []Gap{}}
	inputs, inputKeys := normalizeInputs(opts.Inputs)

	var built []*builtSource
	postmanCandidates := 0
	for _, in := range inputs {
		scanPath, kindHint := in.key, ""
		if isZipInput(in.path) {
			dir, cleanup, err := extractZip(in.path)
			if err != nil {
				report.Inputs = append(report.Inputs, InputReport{Input: in.path, Kind: "zip", Error: err.Error()})
				report.Gaps = append(report.Gaps, inputErrorGap(in.path, err))
				continue
			}
			defer cleanup() // the extracted tree must outlive the loop: copies read from it
			scanPath, kindHint = dir, "zip"
		}
		ir, err := scanInput(in.path, in.key, scanPath, kindHint, opts)
		if err != nil {
			report.Inputs = append(report.Inputs, InputReport{Input: in.path, Error: err.Error()})
			report.Gaps = append(report.Gaps, inputErrorGap(in.path, err))
			continue
		}
		report.Inputs = append(report.Inputs, ir.report)
		report.Gaps = append(report.Gaps, ir.gaps...)
		postmanCandidates += ir.postmanCandidates
		built = append(built, ir.sources...)
	}

	if opts.Name != "" {
		var recommended []*builtSource
		for _, b := range built {
			if b.report.Recommended {
				recommended = append(recommended, b)
			}
		}
		if len(recommended) != 1 {
			return fmt.Errorf("--name is only valid when exactly one source is recommended (got %d)", len(recommended))
		}
		recommended[0].baseName = opts.Name
	}

	sort.Slice(built, func(i, j int) bool {
		return built[i].sortKey() < built[j].sortKey()
	})
	// Refusals here ("--merge cannot reconcile this --out") stay usage errors:
	// the state is intact and the user changes the command. Genuine write-path
	// failures are caught by the --out check above and by writeOutputs.
	prior, err := loadPriorRun(opts, inputKeys, built)
	if err != nil {
		return err
	}
	final := assignNames(opts, built, prior)

	written, err := writeOutputs(opts, final, built, prior, report, postmanCandidates)
	if err != nil {
		return ErrWrite{err: err}
	}
	// The audit artifacts are written either way: "nothing usable" is a result
	// the user needs explained, not just an exit code.
	if written == 0 {
		return ErrNoSources{msg: "no usable API sources written; see GAPS.md"}
	}
	return nil
}

type inputSpec struct {
	path string // as the user spelled it
	key  string // physical absolute path, for identity and scanning
}

// normalizeInputs sorts for determinism and collapses inputs that resolve to the
// same path, which would otherwise be scanned twice into duplicate sources.
func normalizeInputs(raw []string) ([]inputSpec, map[string]bool) {
	sorted := append([]string(nil), raw...)
	sort.Strings(sorted)

	keys := map[string]bool{}
	out := make([]inputSpec, 0, len(sorted))
	for _, in := range sorted {
		key, err := filepath.Abs(in)
		if err != nil {
			key = in
		} else if physical, err := filepath.EvalSymlinks(key); err == nil {
			key = physical
		}
		if keys[key] {
			continue
		}
		keys[key] = true
		out = append(out, inputSpec{path: in, key: key})
	}
	return out, keys
}

func inputErrorGap(input string, err error) Gap {
	return Gap{Kind: gapInputError, Scope: "input", Ref: input,
		Message: err.Error(), Blocking: true}
}

func uniqueName(base string, used map[string]bool) string {
	base = sanitizeName(base)
	if base == "" {
		base = "source"
	}
	if !used[base] {
		return base
	}
	for i := 2; ; i++ {
		cand := fmt.Sprintf("%s_%d", base, i)
		if !used[cand] {
			return cand
		}
	}
}

// Source names become Go package names in the CLI Lathe generates, so they must
// be valid identifiers: underscores rather than hyphens, and never a leading
// digit. A kebab name here fails downstream as `package billing-api`.
func sanitizeName(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	prevSep := false
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			prevSep = false
		default:
			if !prevSep && b.Len() > 0 {
				b.WriteByte('_')
				prevSep = true
			}
		}
	}
	name := strings.Trim(b.String(), "_")
	if name == "" {
		return ""
	}
	if name[0] >= '0' && name[0] <= '9' || goKeywords[name] {
		return "s_" + name
	}
	return name
}

// A source named after a Go keyword compiles no better than a hyphenated one:
// Lathe would emit `package type`.
var goKeywords = map[string]bool{
	"break": true, "case": true, "chan": true, "const": true, "continue": true,
	"default": true, "defer": true, "else": true, "fallthrough": true, "for": true,
	"func": true, "go": true, "goto": true, "if": true, "import": true,
	"interface": true, "map": true, "package": true, "range": true, "return": true,
	"select": true, "struct": true, "switch": true, "type": true, "var": true,
}
