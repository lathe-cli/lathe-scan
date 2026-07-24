// Package scan discovers API specs across one or more repo inputs and emits a
// draft Lathe sources.yaml, a report.json audit, and a human GAPS.md.
//
// It never imports Lathe internals; instead it mirrors Lathe's lenient parse
// and command-emit rules so that "lathe-scan marked this usable" implies "Lathe
// will load and generate from it".
package scan

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

// Options is the parsed CLI invocation.
type Options struct {
	Inputs []string
	Out    string
	Name   string
	Prefer string
	Merge  bool
	Force  bool
	JSON   bool
}

// ErrNoSources maps to exit code 2: nothing usable across all inputs.
type ErrNoSources struct{ msg string }

func (e ErrNoSources) Error() string { return e.msg }

// ErrWrite maps to exit code 3: output could not be written.
type ErrWrite struct{ err error }

func (e ErrWrite) Error() string { return "write output: " + e.err.Error() }
func (e ErrWrite) Unwrap() error { return e.err }

// Execute runs the full scan pipeline for the given options.
func Execute(opts Options) error {
	if strings.TrimSpace(opts.Out) == "" {
		return fmt.Errorf("--out is required")
	}
	if opts.Name != "" && len(opts.Inputs) != 1 {
		return fmt.Errorf("--name is only valid with a single input")
	}

	report := &Report{SchemaVersion: 1, ToolVersion: version}
	usedNames := map[string]bool{}

	// --merge: keep foreign sources already present in the output.
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

	// Deterministic input order.
	inputs := append([]string(nil), opts.Inputs...)
	sort.Strings(inputs)

	var built []*builtSource
	for _, in := range inputs {
		ir, err := scanInput(in, opts)
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

	// Deterministic naming: order by (baseName, source path), then resolve collisions.
	sort.Slice(built, func(i, j int) bool {
		if built[i].baseName != built[j].baseName {
			return built[i].baseName < built[j].baseName
		}
		return built[i].copyFrom+strings.Join(built[i].files, ",") < built[j].copyFrom+strings.Join(built[j].files, ",")
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

// uniqueName resolves collisions deterministically with a numeric suffix.
func uniqueName(base string, used map[string]bool) string {
	base = sanitizeName(base)
	if base == "" {
		base = "source"
	}
	if !used[base] {
		return base
	}
	for i := 2; ; i++ {
		cand := fmt.Sprintf("%s-%d", base, i)
		if !used[cand] {
			return cand
		}
	}
}

func sanitizeName(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	prevDash := false
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			prevDash = false
		default:
			if !prevDash && b.Len() > 0 {
				b.WriteByte('-')
				prevDash = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}
