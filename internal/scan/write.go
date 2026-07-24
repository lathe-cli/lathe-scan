package scan

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	sourcesFileName = "sources.yaml"
	reportFileName  = "report.json"
	gapsFileName    = "GAPS.md"
)

// sourcesFile mirrors the top-level Lathe sourceconfig shape. Foreign sources
// (backends we do not model, e.g. proto/graphql) are preserved as raw nodes on
// --merge.
type sourcesFile struct {
	Sources map[string]yaml.Node `yaml:"sources"`
}

// ycSource mirrors Lathe's Source yaml tags exactly for the backends this slice
// emits (openapi3, swagger). omitempty keeps mutually-exclusive origin fields off.
type ycSource struct {
	DisplayName     string      `yaml:"display_name,omitempty"`
	DefaultHostname string      `yaml:"default_hostname,omitempty"`
	RepoURL         string      `yaml:"repo_url,omitempty"`
	PinnedTag       string      `yaml:"pinned_tag,omitempty"`
	LocalPath       string      `yaml:"local_path,omitempty"`
	Backend         string      `yaml:"backend"`
	Swagger         *filesBlock `yaml:"swagger,omitempty"`
	OpenAPI3        *filesBlock `yaml:"openapi3,omitempty"`
}

type filesBlock struct {
	Files []string `yaml:"files"`
}

func loadExistingSources(path string) (*sourcesFile, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return &sourcesFile{Sources: map[string]yaml.Node{}}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read existing %s: %w", path, err)
	}
	var sf sourcesFile
	if err := yaml.Unmarshal(data, &sf); err != nil {
		return nil, fmt.Errorf("parse existing %s: %w", path, err)
	}
	if sf.Sources == nil {
		sf.Sources = map[string]yaml.Node{}
	}
	return &sf, nil
}

func writeOutputs(opts Options, existing *sourcesFile, built []*builtSource, report *Report) error {
	if err := prepareOutDir(opts, len(built)); err != nil {
		return err
	}

	final := map[string]yaml.Node{}
	if existing != nil {
		for k, v := range existing.Sources {
			final[k] = v
		}
	}

	for _, b := range built {
		yc := &ycSource{
			DefaultHostname: b.hostname,
			Backend:         b.backend,
		}
		switch b.origin.Type {
		case "repo_url":
			yc.RepoURL = b.origin.RepoURL
			yc.PinnedTag = b.origin.PinnedTag
		default:
			yc.LocalPath = b.Name
			if err := copyLocal(b, filepath.Join(opts.Out, b.Name)); err != nil {
				return err
			}
			b.origin.LocalPath = b.Name
		}
		switch b.backend {
		case "openapi3":
			yc.OpenAPI3 = &filesBlock{Files: b.files}
		case "swagger":
			yc.Swagger = &filesBlock{Files: b.files}
		}

		var node yaml.Node
		if err := node.Encode(yc); err != nil {
			return fmt.Errorf("encode source %q: %w", b.Name, err)
		}
		final[b.Name] = node

		b.report.Name = b.Name
		b.report.Origin = b.origin
		report.Sources = append(report.Sources, *b.report)
	}

	fillReportMeta(opts, built, report)

	if err := writeYAML(filepath.Join(opts.Out, sourcesFileName), &sourcesFile{Sources: final}); err != nil {
		return err
	}
	if err := writeJSON(filepath.Join(opts.Out, reportFileName), report); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(opts.Out, gapsFileName), []byte(renderGaps(report)), 0o644); err != nil {
		return err
	}

	if opts.JSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(report)
	}
	return nil
}

func prepareOutDir(opts Options, nBuilt int) error {
	info, err := os.Stat(opts.Out)
	switch {
	case os.IsNotExist(err):
		return os.MkdirAll(opts.Out, 0o755)
	case err != nil:
		return err
	case !info.IsDir():
		return fmt.Errorf("--out is not a directory: %s", opts.Out)
	}
	if opts.Force || opts.Merge {
		return nil
	}
	entries, err := os.ReadDir(opts.Out)
	if err != nil {
		return err
	}
	if len(entries) > 0 {
		return fmt.Errorf("--out %s is not empty; use --force to overwrite or --merge to combine", opts.Out)
	}
	return nil
}

func copyLocal(b *builtSource, destDir string) error {
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return err
	}
	data, err := os.ReadFile(b.copyFrom)
	if err != nil {
		return fmt.Errorf("read source file %s: %w", b.copyFrom, err)
	}
	dest := filepath.Join(destDir, b.copyTo)
	if err := os.WriteFile(dest, data, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", dest, err)
	}
	return nil
}

func fillReportMeta(opts Options, built []*builtSource, report *Report) {
	usable := 0
	for i := range report.Sources {
		if report.Sources[i].Confidence != confLow || report.Sources[i].WouldEmitCommands > 0 {
			usable++
		}
	}
	report.Summary = Summary{
		Inputs:   len(report.Inputs),
		Sources:  len(report.Sources),
		Usable:   usable,
		ExitCode: exitCodeFor(built),
	}
	// Selected names per input.
	byInput := map[string][]string{}
	for _, b := range built {
		byInput[b.fromInput] = append(byInput[b.fromInput], b.Name)
	}
	for i := range report.Inputs {
		names := byInput[report.Inputs[i].Input]
		sort.Strings(names)
		report.Inputs[i].Selected = names
	}
}

func exitCodeFor(built []*builtSource) int {
	if len(built) == 0 {
		return 2
	}
	return 0
}

func writeYAML(path string, v any) error {
	data, err := yaml.Marshal(v)
	if err != nil {
		return fmt.Errorf("marshal yaml: %w", err)
	}
	return os.WriteFile(path, data, 0o644)
}

func writeJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal json: %w", err)
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

func renderGaps(report *Report) string {
	var b strings.Builder
	b.WriteString("# lathe-scan gaps\n\n")
	b.WriteString(fmt.Sprintf("%d input(s), %d source(s), %d usable.\n\n",
		report.Summary.Inputs, report.Summary.Sources, report.Summary.Usable))

	b.WriteString("## Sources\n\n")
	for _, s := range report.Sources {
		star := ""
		if s.Recommended {
			star = " (recommended)"
		}
		b.WriteString(fmt.Sprintf("- **%s**%s — backend `%s`, confidence `%s`, %d command(s)\n",
			s.Name, star, s.Backend, s.Confidence, s.WouldEmitCommands))
		if s.Origin != nil {
			if s.Origin.Type == "repo_url" {
				b.WriteString(fmt.Sprintf("  - origin: `%s` @ `%s` (%s)\n", s.Origin.RepoURL, s.Origin.PinnedTag, s.Origin.RefKind))
			} else {
				b.WriteString(fmt.Sprintf("  - origin: local_path `%s`\n", s.Origin.LocalPath))
			}
		}
	}

	// Blocking gaps first.
	var blocking, advisory []string
	for _, s := range report.Sources {
		for _, g := range s.Gaps {
			line := fmt.Sprintf("- `%s` [%s %s] %s", g.Kind, g.Scope, s.Name, g.Message)
			if g.Blocking {
				blocking = append(blocking, line)
			} else {
				advisory = append(advisory, line)
			}
		}
	}
	if len(blocking) > 0 {
		b.WriteString("\n## Blocking — must resolve before generating\n\n")
		b.WriteString(strings.Join(blocking, "\n") + "\n")
	}
	if len(advisory) > 0 {
		b.WriteString("\n## Advisory\n\n")
		b.WriteString(strings.Join(advisory, "\n") + "\n")
	}

	b.WriteString("\n## Next\n\n")
	b.WriteString("Review sources and origins above, then point Lathe at this directory:\n\n")
	b.WriteString("```sh\nlathe sync-specs && lathe gen\n```\n")
	return b.String()
}
