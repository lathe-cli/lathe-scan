package cli

import (
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/lathe-cli/lathe-scan/internal/scan"
)

// Exit codes are part of the CLI contract:
//
//	0 at least one usable source written
//	1 usage error
//	2 nothing usable found or extracted across all inputs
//	3 write failure
const (
	exitOK        = 0
	exitUsage     = 1
	exitNoSources = 2
	exitWrite     = 3
)

func Run(args []string) int {
	var opts scan.Options

	cmd := &cobra.Command{
		Use:   "lathe-scan <input>... --out <dir>",
		Short: "Discover API specs across repos and emit a draft Lathe sources.yaml",
		Long: "lathe-scan reads one or more repo directories or zip archives (offline, " +
			"read-only), recommends one candidate per logical API, and aggregates those " +
			"recommendations into a draft Lathe sources.yaml, plus report.json and GAPS.md " +
			"for human confirmation.",
		// tool_version in report.json is the audit anchor; the binary must be able
		// to state which version produced a given report.
		Version:       scan.Version(),
		Args:          cobra.MinimumNArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(_ *cobra.Command, inputs []string) error {
			opts.Inputs = inputs
			return scan.Execute(opts)
		},
	}

	f := cmd.Flags()
	f.StringVar(&opts.Out, "out", "", "output directory for sources.yaml, report.json, GAPS.md (required)")
	f.StringVar(&opts.Name, "name", "", "source name; only valid when exactly one source is recommended")
	f.StringVar(&opts.Prefer, "prefer", "", "preferred backend on ties: openapi3|swagger|proto|graphql")
	f.BoolVar(&opts.Merge, "merge", false, "fold results into an existing --out/sources.yaml, preserving foreign sources")
	f.BoolVar(&opts.Force, "force", false, "allow overwriting a non-empty --out")
	f.BoolVar(&opts.JSON, "json", false, "print report.json to stdout")
	_ = cmd.MarkFlagRequired("out")

	cmd.SetArgs(args)
	err := cmd.Execute()
	if err == nil {
		return exitOK
	}

	var noSrc scan.ErrNoSources
	var write scan.ErrWrite
	switch {
	case errors.As(err, &noSrc):
		fmt.Fprintln(os.Stderr, "lathe-scan:", err)
		return exitNoSources
	case errors.As(err, &write):
		fmt.Fprintln(os.Stderr, "lathe-scan:", err)
		return exitWrite
	default:
		fmt.Fprintln(os.Stderr, "lathe-scan:", err)
		return exitUsage
	}
}
