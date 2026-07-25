// Package scan discovers API specs and emits a draft Lathe sources.yaml.
//
// It never imports Lathe internals; it mirrors Lathe's lenient parse and
// command-emit rules so "usable" here implies Lathe will load and generate.
package scan

import (
	"fmt"
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

func Execute(opts Options) error {
	if strings.TrimSpace(opts.Out) == "" {
		return fmt.Errorf("--out is required")
	}
	if opts.Name != "" && len(opts.Inputs) != 1 {
		return fmt.Errorf("--name is only valid with a single input")
	}

	report := &Report{SchemaVersion: 1, ToolVersion: version}
	usedNames := map[string]bool{}

	// --merge keeps foreign sources already present in the output.
	var existing *sourcesFile
	if opts.Merge {
		var err error
		existing, err = loadExistingSources(filepath.Join(opts.Out, sourcesFileName))
		if err != nil {
			return err
		}
		for name := range existing.Sources {
			usedNames[name] = true
		}
	}

	inputs := append([]string(nil), opts.Inputs...)
	sort.Strings(inputs)

	var built []*builtSource
	for _, in := range inputs {
		scanPath, kindHint := in, ""
		if isZipInput(in) {
			dir, cleanup, err := extractZip(in)
			if err != nil {
				report.Inputs = append(report.Inputs, InputReport{Input: in, Kind: "zip", Error: err.Error()})
				continue
			}
			defer cleanup()
			scanPath, kindHint = dir, "zip"
		}
		ir, err := scanInput(in, scanPath, kindHint, opts)
		if err != nil {
			report.Inputs = append(report.Inputs, InputReport{Input: in, Error: err.Error()})
			continue
		}
		report.Inputs = append(report.Inputs, ir.report)
		built = append(built, ir.sources...)
	}

	if opts.Name != "" {
		if len(built) != 1 {
			return fmt.Errorf("--name is only valid when exactly one source is produced (got %d)", len(built))
		}
		built[0].baseName = opts.Name
	}

	sort.Slice(built, func(i, j int) bool {
		return built[i].sortKey() < built[j].sortKey()
	})
	for _, b := range built {
		b.Name = uniqueName(b.baseName, usedNames)
		usedNames[b.Name] = true
	}

	if len(built) == 0 && (existing == nil || len(existing.Sources) == 0) {
		return ErrNoSources{msg: "no usable API sources found across all inputs"}
	}

	if err := writeOutputs(opts, existing, built, report); err != nil {
		return ErrWrite{err: err}
	}
	return nil
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
