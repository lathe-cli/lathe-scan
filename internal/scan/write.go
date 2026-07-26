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

// Foreign sources (backends we did not build this run) are preserved as raw
// nodes on --merge.
type sourcesFile struct {
	Sources map[string]yaml.Node `yaml:"sources"`
}

// ycSource mirrors Lathe's Source yaml tags. omitempty keeps mutually-exclusive
// origin fields and non-selected backend blocks off.
type ycSource struct {
	DisplayName     string        `yaml:"display_name,omitempty"`
	DefaultHostname string        `yaml:"default_hostname,omitempty"`
	RepoURL         string        `yaml:"repo_url,omitempty"`
	PinnedTag       string        `yaml:"pinned_tag,omitempty"`
	LocalPath       string        `yaml:"local_path,omitempty"`
	Backend         string        `yaml:"backend"`
	Swagger         *filesBlock   `yaml:"swagger,omitempty"`
	Proto           *protoBlock   `yaml:"proto,omitempty"`
	OpenAPI3        *filesBlock   `yaml:"openapi3,omitempty"`
	GraphQL         *graphqlBlock `yaml:"graphql,omitempty"`
}

type filesBlock struct {
	Files []string `yaml:"files"`
}

type protoBlock struct {
	Staging     []stagingEntry `yaml:"staging"`
	Entries     []string       `yaml:"entries"`
	ImportRoots []string       `yaml:"import_roots,omitempty"`
}

type stagingEntry struct {
	From string `yaml:"from"`
	To   string `yaml:"to"`
}

type graphqlBlock struct {
	Schema string        `yaml:"schema"`
	Expose graphqlExpose `yaml:"expose"`
}

type graphqlExpose struct {
	Queries   []string `yaml:"queries,omitempty"`
	Mutations []string `yaml:"mutations,omitempty"`
}

// priorRun is what an earlier run left in --out. Copied directories from earlier
// runs are deliberately never deleted: report.json is an ordinary editable file,
// and deriving deletions from it lets a corrupted report decide what is removed —
// `local_path: "."` alone would take out the whole tree.
type priorRun struct {
	carried map[string]yaml.Node   // entries to keep: foreign, plus ours from inputs not rescanned
	ownedBy map[string]*Provenance // carried name -> provenance, so ownership survives the round trip
	byProv  map[string]string      // provenance key -> the name we gave it last time
	// priorFor is keyed by provenance, never by name: a name is reusable, and a
	// different API inheriting the freed name must not inherit the old entry's
	// hand-written policy with it.
	priorFor map[string]yaml.Node
	// kept: entries whose input produced nothing this run. Scan cannot tell "this
	// API is gone" from "this input did not answer", and only the second reading is
	// recoverable — a mistyped path must not delete a working entry plus the policy
	// a human wrote onto it. An input that did produce sources answered, so entries
	// it no longer accounts for really are gone.
	kept map[string]*Provenance
}

func provKey(p *Provenance) string {
	return p.Input + "\x00" + p.Backend + "\x00" + p.Key
}

func loadPriorRun(opts Options, inputKeys map[string]bool, built []*builtSource) (*priorRun, error) {
	pr := &priorRun{
		carried:  map[string]yaml.Node{},
		ownedBy:  map[string]*Provenance{},
		byProv:   map[string]string{},
		priorFor: map[string]yaml.Node{},
		kept:     map[string]*Provenance{},
	}

	if !opts.Merge {
		return pr, nil
	}

	// The manifest is read first because it decides how severe an unreadable
	// report is: with entries on disk, a report we cannot parse means ownership is
	// unrecoverable; with no entries there is nothing to own.
	sf, err := loadExistingSources(filepath.Join(opts.Out, sourcesFileName))
	if err != nil {
		return nil, err
	}

	// Ownership comes from both sources[] and preserved[]: reading only the first
	// loses every source after the run that stopped rebuilding it, so scanning A,
	// then B, then A again would no longer recognize A's own entry.
	ownedByName := map[string]*Provenance{}
	prev, rerr := loadPreviousReport(filepath.Join(opts.Out, reportFileName))
	if rerr != nil && len(sf.Sources) > 0 {
		// "Absent" is a legitimate state (a hand-written manifest); "present but
		// unreadable" is a damaged one. Carrying on would treat every owned entry as
		// foreign and append a duplicate beside it on the next scan — exactly the
		// loss --merge exists to prevent.
		return nil, fmt.Errorf("%s could not be read (%v), so --merge cannot tell which of the %d entries in %s it wrote and would append duplicates; restore or delete %s, or re-run without --merge to rebuild the manifest from this scan (add --force to overwrite a non-empty --out)",
			filepath.Join(opts.Out, reportFileName), rerr, len(sf.Sources), sourcesFileName, reportFileName)
	}
	unowned := 0
	for _, s := range prev.Sources {
		// A sources[] entry without provenance predates the field.
		if s.Provenance == nil {
			unowned++
			continue
		}
		pr.byProv[provKey(s.Provenance)] = s.Name
		ownedByName[s.Name] = s.Provenance
	}
	// In preserved[], absent provenance means a foreign entry, not an old format.
	for _, s := range prev.Preserved {
		if s.Provenance != nil {
			pr.byProv[provKey(s.Provenance)] = s.Name
			ownedByName[s.Name] = s.Provenance
		}
	}

	// A pre-provenance report cannot say which manifest entries are ours, and the
	// old format recorded nothing that lets ownership be reconstructed. Guessing
	// would re-point a name at a different API; carrying on would append billing_2
	// beside billing — the duplication --merge exists to prevent. Refuse.
	if unowned > 0 && len(sf.Sources) > 0 {
		return nil, fmt.Errorf("%s was written by a lathe-scan that did not record source ownership (%d of %d entries in %s have no provenance), so --merge cannot tell which %s entries are its own and would append duplicates; re-run without --merge to rebuild the manifest from this scan (add --force to overwrite a non-empty --out), then re-apply any policy hand-written into the old entries",
			opts.Out, unowned, len(prev.Sources), reportFileName, sourcesFileName)
	}

	produced := map[string]bool{}
	for _, b := range built {
		produced[b.inputKey] = true
	}
	for name, node := range sf.Sources {
		// Ours from a rescanned input is rebuilt, not carried — carrying the old
		// copy forward is what duplicated sources on every re-merge.
		if p, ok := ownedByName[name]; ok && inputKeys[p.Input] {
			pr.priorFor[provKey(p)] = node
			if !produced[p.Input] {
				pr.kept[name] = p
			}
			continue
		}
		pr.carried[name] = node
		if p, ok := ownedByName[name]; ok {
			pr.ownedBy[name] = p
		}
	}
	return pr, nil
}

// loadPreviousReport reads this tool's own report from --out. A missing report
// means "no prior run to reconcile with" and is not an error; anything else is
// returned so the caller can decide, since whether a damaged report is fatal
// depends on there being a manifest whose ownership it alone can explain.
func loadPreviousReport(path string) (*Report, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return &Report{}, nil
	}
	if err != nil {
		return &Report{}, err
	}
	var r Report
	if err := json.Unmarshal(data, &r); err != nil {
		return &Report{}, err
	}
	return &r, nil
}

// assignNames reuses the name a previous run gave each source so --merge updates
// entries in place, and only then falls back to fresh, collision-free names.
//
// A source name is not only a manifest key: a local_path source also copies its
// files into <out>/<name>. So the names already spoken for include every entry
// in the manifest, every directory those entries point at, and every directory
// already sitting in --out — including ones left by an earlier run that no
// manifest mentions. Reusing one of those silently overwrites data this run did
// not create, which is the same harm as deleting it.
func assignNames(opts Options, built []*builtSource, prior *priorRun) map[string]yaml.Node {
	final := map[string]yaml.Node{}
	used := map[string]bool{}
	for name, node := range prior.carried {
		final[name] = node
		used[name] = true
	}
	// A kept entry holds its name as firmly as a carried one: hand it to a source
	// from another input and the entry we set out to protect is overwritten.
	for name, p := range prior.kept {
		final[name] = prior.priorFor[provKey(p)]
		used[name] = true
	}

	// Reuse-in-place first, so a source that owns <out>/<name> keeps it and is
	// unaffected by the reservations added below.
	pending := built[:0:0]
	for _, b := range built {
		name, ok := prior.byProv[provKey(b.provenance())]
		if !ok || used[name] {
			pending = append(pending, b)
			continue
		}
		b.Name = name
		used[name] = true
	}

	for _, name := range occupiedNames(opts, prior) {
		used[name] = true
	}
	for _, b := range pending {
		b.Name = uniqueName(b.baseName, used)
		used[b.Name] = true
	}
	return final
}

// occupiedNames lists names a new source must not take: directories referenced
// by entries we are carrying, and whatever already exists in --out. ReadDir is
// used rather than a stat per candidate because it names what is there without
// dereferencing anything — a dangling symlink still occupies its name.
func occupiedNames(opts Options, prior *priorRun) []string {
	var out []string
	for _, node := range prior.carried {
		if v := mapGet(&node, "local_path"); v != nil && v.Value != "" {
			out = append(out, filepath.Base(filepath.Clean(v.Value)))
		}
	}
	entries, err := os.ReadDir(opts.Out)
	if err != nil {
		return out
	}
	for _, e := range entries {
		name := e.Name()
		if name == sourcesFileName || name == reportFileName || name == gapsFileName {
			continue
		}
		out = append(out, name)
	}
	return out
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

func writeOutputs(opts Options, final map[string]yaml.Node, built []*builtSource, prior *priorRun, report *Report, postmanCandidates int) (written int, err error) {
	if err := prepareOutDir(opts); err != nil {
		return 0, err
	}

	for _, b := range built {
		// Resolved before anything is copied: a source we decline to update must
		// not leave new material on disk either.
		if gap, ok := blockedByPolicy(b, prior); !ok {
			final[b.Name] = prior.priorFor[provKey(b.provenance())]
			report.Preserved = append(report.Preserved, PreservedSource{Name: b.Name, Provenance: b.provenance()})
			report.Gaps = append(report.Gaps, gap)
			continue
		}

		if b.origin.Type == "local_path" {
			b.yc.LocalPath = b.Name
			b.origin.LocalPath = b.Name
			if err := copyLocal(b, filepath.Join(opts.Out, b.Name)); err != nil {
				return 0, err
			}
		}

		node, err := encodeSource(b, prior)
		if err != nil {
			return 0, err
		}
		final[b.Name] = node

		b.report.Name = b.Name
		b.report.Origin = b.origin
		b.report.Input = b.fromInput
		b.report.Provenance = b.provenance()
		report.Sources = append(report.Sources, *b.report)
	}

	for name := range prior.carried {
		report.Preserved = append(report.Preserved, PreservedSource{Name: name, Provenance: prior.ownedBy[name]})
	}
	for name, p := range prior.kept {
		report.Preserved = append(report.Preserved, PreservedSource{Name: name, Provenance: p})
		report.Gaps = append(report.Gaps, Gap{Kind: gapSourceKept, Scope: "source", Ref: name,
			Message:  "the input that wrote this entry produced no usable source in this run; kept the entry as it was rather than removing it — resolve the blocking gap above, or delete the entry yourself if that API is gone",
			Blocking: false})
	}
	sort.Slice(report.Preserved, func(i, j int) bool { return report.Preserved[i].Name < report.Preserved[j].Name })
	fillReportMeta(report, postmanCandidates)

	sourcesPath := filepath.Join(opts.Out, sourcesFileName)
	if len(final) > 0 {
		data, merr := yaml.Marshal(&sourcesFile{Sources: final})
		if merr != nil {
			return 0, fmt.Errorf("marshal %s: %w", sourcesFileName, merr)
		}
		if err := writeFileAtomic(sourcesPath, data); err != nil {
			return 0, err
		}
	} else if rerr := os.Remove(sourcesPath); rerr != nil && !os.IsNotExist(rerr) {
		// A run that produced nothing must not leave an earlier manifest behind:
		// it would describe sources this scan just contradicted.
		return 0, fmt.Errorf("remove stale %s: %w", sourcesFileName, rerr)
	}

	// GAPS.md first: it never states the exit code, so it needs no revisiting if
	// the last delivery below fails.
	if err := writeFileAtomic(filepath.Join(opts.Out, gapsFileName), []byte(renderGaps(report))); err != nil {
		return 0, err
	}
	reportPath := filepath.Join(opts.Out, reportFileName)
	if err := writeReport(reportPath, report); err != nil {
		return 0, err
	}

	if opts.JSON {
		// --json makes stdout a machine interface, so a failed write is a failed
		// delivery, not a cosmetic problem. Reporting it means the exit code no
		// longer matches the report already on disk, and summary.exit_code is
		// contractually the process result — so correct the file before returning.
		// The report cannot state the outcome of its own delivery in one pass;
		// stdout goes last precisely so this is the only case needing a second write.
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(report); err != nil {
			report.Summary.ExitCode = exitWriteFailure
			if rerr := writeReport(reportPath, report); rerr != nil {
				return 0, fmt.Errorf("write report to stdout: %w (and could not record the failure in %s: %v)", err, reportFileName, rerr)
			}
			return 0, fmt.Errorf("write report to stdout: %w", err)
		}
	}
	return len(report.Sources), nil
}

func writeReport(path string, report *Report) error {
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal json: %w", err)
	}
	return writeFileAtomic(path, append(data, '\n'))
}

// Keys scan derives. Everything else in a source entry belongs to whoever wrote
// it, and --merge must hand it back untouched — DESIGN names groups, output and
// selection as human policy, and a user can add more than that.
var (
	ownedSourceKeys = []string{"default_hostname", "repo_url", "pinned_tag", "local_path", "backend"}
	backendBlocks   = []string{"openapi3", "swagger", "proto", "graphql"}
	ownedBlockKeys  = map[string][]string{
		"openapi3": {"files"},
		"swagger":  {"files"},
		"proto":    {"staging", "entries", "import_roots"},
		"graphql":  {"schema", "expose"},
	}
)

// encodeSource renders one source. On --merge it overwrites only the keys scan
// owns inside the entry that is already there, so hand-written policy survives
// by default instead of by enumeration: decoding into ycSource and re-encoding
// would drop every field this struct does not model.
func encodeSource(b *builtSource, prior *priorRun) (yaml.Node, error) {
	var fresh yaml.Node
	if err := fresh.Encode(b.yc); err != nil {
		return fresh, fmt.Errorf("encode source %q: %w", b.Name, err)
	}
	old, ok := prior.priorFor[provKey(b.provenance())]
	if !ok || old.Kind != yaml.MappingNode {
		return fresh, nil
	}

	carryTrimmedExpose(b, &old, &fresh)

	merged := old
	for _, k := range ownedSourceKeys {
		mapSet(&merged, k, mapGet(&fresh, k))
	}
	for _, block := range backendBlocks {
		if block != b.yc.Backend {
			mapSet(&merged, block, nil) // Lathe rejects a block that is not the backend
			continue
		}
		freshBlock, oldBlock := mapGet(&fresh, block), mapGet(&merged, block)
		if oldBlock == nil || oldBlock.Kind != yaml.MappingNode || freshBlock == nil {
			mapSet(&merged, block, freshBlock)
			continue
		}
		for _, k := range ownedBlockKeys[block] {
			mapSet(oldBlock, k, mapGet(freshBlock, k))
		}
	}
	return merged, nil
}

// carryTrimmedExpose keeps a graphql.expose the user narrowed. Scan lists every
// discovered query and mutation and tells them to trim it, so restoring the full
// list on the next merge would re-expose a surface they deliberately removed.
func carryTrimmedExpose(b *builtSource, old, fresh *yaml.Node) {
	if b.yc.GraphQL == nil {
		return
	}
	prev, ok, _ := priorExpose(old)
	if !ok {
		return
	}
	kept := graphqlExpose{
		Queries:   intersect(prev.Queries, b.yc.GraphQL.Expose.Queries),
		Mutations: intersect(prev.Mutations, b.yc.GraphQL.Expose.Mutations),
	}
	// Set equality, not lengths: a manifest that lists an operation twice makes
	// the counts match the full discovered surface while naming fewer operations,
	// and "same size" would then read as "not trimmed" and publish everything the
	// user removed.
	if sameSet(kept.Queries, b.yc.GraphQL.Expose.Queries) &&
		sameSet(kept.Mutations, b.yc.GraphQL.Expose.Mutations) {
		return // not trimmed; nothing to preserve
	}
	var node yaml.Node
	if err := node.Encode(kept); err != nil {
		return
	}
	if block := mapGet(fresh, "graphql"); block != nil {
		mapSet(block, "expose", &node)
	}
	b.report.Gaps = append(b.report.Gaps, Gap{Kind: gapExposePreserved, Scope: "source",
		Message: fmt.Sprintf("kept the trimmed graphql.expose from the existing manifest (%d quer(ies), %d mutation(s)); re-check it against the current schema",
			len(kept.Queries), len(kept.Mutations)), Blocking: false})
}

// blockedByPolicy refuses to rewrite an entry when doing so would widen what the
// user chose to expose. If a trimmed graphql.expose no longer intersects the
// schema at all, every remembered decision is stale — writing the freshly
// discovered surface would silently publish operations nobody approved. Fail
// closed: leave the entry exactly as it is and say so.
func blockedByPolicy(b *builtSource, prior *priorRun) (Gap, bool) {
	old, ok := prior.priorFor[provKey(b.provenance())]
	if !ok || b.yc.GraphQL == nil {
		return Gap{}, true
	}
	prev, ok, err := priorExpose(&old)
	if err != nil {
		// Policy we cannot read is policy we cannot honor: fail closed like a
		// stale one.
		return Gap{Kind: gapExposeUnreadable, Scope: "source", Ref: b.Name,
			Message:  fmt.Sprintf("graphql.expose in the existing manifest could not be read (%v); left the entry untouched rather than replacing it with the discovered surface — fix it, then re-run", err),
			Blocking: true}, false
	}
	if !ok {
		return Gap{}, true
	}
	prevCount := len(prev.Queries) + len(prev.Mutations)
	if prevCount == 0 {
		// Lathe needs at least one operation: writing the discovered surface would
		// widen the expose, writing it back would claim a usable source that is not.
		return Gap{Kind: gapExposeEmpty, Scope: "source", Ref: b.Name,
			Message:  "graphql.expose in the existing manifest is empty; Lathe requires at least one query or mutation, and scan will not choose the surface for you — set it, then re-run",
			Blocking: true}, false
	}
	kept := len(intersect(prev.Queries, b.yc.GraphQL.Expose.Queries)) +
		len(intersect(prev.Mutations, b.yc.GraphQL.Expose.Mutations))
	if kept > 0 {
		return Gap{}, true
	}
	return Gap{Kind: gapExposeStale, Scope: "source", Ref: b.Name,
		Message: fmt.Sprintf("the manifest exposes %d operation(s) that the current schema no longer declares; left the entry untouched rather than replacing it with the %d newly discovered operation(s) — re-confirm the expose policy",
			prevCount, len(b.yc.GraphQL.Expose.Queries)+len(b.yc.GraphQL.Expose.Mutations)),
		Blocking: true}, false
}

// priorExpose decodes only the graphql.expose subtree: an unrelated malformed
// key elsewhere in the entry must not decide the fate of an exposure policy.
// ok reports whether a remembered policy exists; err means it cannot be read.
func priorExpose(entry *yaml.Node) (e graphqlExpose, ok bool, err error) {
	node := mapGet(mapGet(entry, "graphql"), "expose")
	if node == nil {
		return e, false, nil
	}
	if err := node.Decode(&e); err != nil {
		return graphqlExpose{}, false, err
	}
	return e, true, nil
}

// intersect returns the members of want that exist in available, each once.
// De-duplicating matters: these results are compared against the discovered
// surface to decide whether the user narrowed it, and a repeated entry would
// otherwise inflate the count past what it actually names.
func intersect(want, available []string) []string {
	have := make(map[string]bool, len(available))
	for _, s := range available {
		have[s] = true
	}
	seen := make(map[string]bool, len(want))
	var out []string
	for _, s := range want {
		if have[s] && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func sameSet(a, b []string) bool {
	set := make(map[string]bool, len(a))
	for _, s := range a {
		set[s] = true
	}
	for _, s := range b {
		if !set[s] {
			return false
		}
		delete(set, s)
	}
	return len(set) == 0
}

func mapGet(n *yaml.Node, key string) *yaml.Node {
	if n == nil || n.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return n.Content[i+1]
		}
	}
	return nil
}

// mapSet replaces key in place (keeping the human's key order) or appends it. A
// nil value deletes the key.
func mapSet(n *yaml.Node, key string, val *yaml.Node) {
	if n == nil || n.Kind != yaml.MappingNode {
		return
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value != key {
			continue
		}
		if val == nil {
			n.Content = append(n.Content[:i], n.Content[i+2:]...)
			return
		}
		n.Content[i+1] = val
		return
	}
	if val != nil {
		n.Content = append(n.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, val)
	}
}

func prepareOutDir(opts Options) error {
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
	for _, c := range b.copies {
		data, err := os.ReadFile(c.absFrom)
		if err != nil {
			return fmt.Errorf("read source file %s: %w", c.absFrom, err)
		}
		if err := writeUnder(destDir, c.relTo, data); err != nil {
			return err
		}
	}
	for _, s := range b.synth {
		if err := writeUnder(destDir, s.relTo, s.content); err != nil {
			return err
		}
	}
	return nil
}

// writeUnder writes one file inside destDir, refusing to follow a symlink out of
// it. Checking only the final path is not enough: with out/pkg/schemas already a
// symlink elsewhere, MkdirAll succeeds and WriteFile lands outside --out. Every
// existing component from destDir down is checked, and the target itself with
// Lstat, since Stat calls a dangling symlink "absent" and then writes through it.
func writeUnder(destDir, relTo string, data []byte) error {
	dest := filepath.Join(destDir, filepath.FromSlash(relTo))
	if link, err := containsSymlink(destDir, dest); err != nil {
		return fmt.Errorf("write %s: %w", dest, err)
	} else if link != "" {
		return fmt.Errorf("write %s: %s is a symlink; refusing to write through it", dest, link)
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	if info, err := os.Lstat(dest); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("write %s: target is a symlink; refusing to write through it", dest)
	}
	if err := os.WriteFile(dest, data, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", dest, err)
	}
	return nil
}

func fillReportMeta(report *Report, postmanCandidates int) {
	// [] rather than null: an empty list is a cleaner contract for a consumer.
	for i := range report.Sources {
		if report.Sources[i].Gaps == nil {
			report.Sources[i].Gaps = []Gap{}
		}
	}
	report.Summary = Summary{
		Inputs:            len(report.Inputs),
		Sources:           len(report.Sources),
		Usable:            len(report.Sources),
		PostmanCandidates: postmanCandidates,
		ExitCode:          exitCodeFor(len(report.Sources)),
	}
	byInput := map[string][]string{}
	for _, s := range report.Sources {
		byInput[s.Input] = append(byInput[s.Input], s.Name)
	}
	for i := range report.Inputs {
		names := byInput[report.Inputs[i].Input]
		sort.Strings(names)
		report.Inputs[i].Selected = names
	}
	sortGaps(report.Gaps)
}

// sortGaps keeps report.json byte-identical across runs: gaps arrive per input,
// but blocking-first ordering is what GAPS.md renders from.
func sortGaps(gaps []Gap) {
	sort.SliceStable(gaps, func(i, j int) bool {
		a, b := gaps[i], gaps[j]
		if a.Blocking != b.Blocking {
			return a.Blocking
		}
		if a.Scope != b.Scope {
			return a.Scope < b.Scope
		}
		if a.Ref != b.Ref {
			return a.Ref < b.Ref
		}
		return a.Kind < b.Kind
	})
}

// Mirrors internal/cli's exit contract; report.summary.exit_code has to agree
// with what the process actually returns.
const (
	exitOKCode       = 0
	exitNoSourceCode = 2
	exitWriteFailure = 3
)

// A source left untouched to protect existing policy does not count as written:
// the run handed the user nothing new.
func exitCodeFor(written int) int {
	if written == 0 {
		return exitNoSourceCode
	}
	return exitOKCode
}

// writeFileAtomic replaces one file in a single rename, so a failed or
// interrupted write never leaves a truncated artifact for the user to confirm.
// It says nothing about the three artifacts as a set: a crash between them can
// still leave sources.yaml newer than report.json.
func writeFileAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp")
	if err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	tmpName := tmp.Name()
	_, werr := tmp.Write(data)
	if cerr := tmp.Close(); werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = os.Chmod(tmpName, 0o644)
	}
	if werr == nil {
		werr = os.Rename(tmpName, path)
	}
	if werr != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("write %s: %w", path, werr)
	}
	return nil
}

func renderGaps(report *Report) string {
	var b strings.Builder
	b.WriteString("# lathe-scan gaps\n\n")
	fmt.Fprintf(&b, "%d input(s), %d source(s), %d usable.\n\n",
		report.Summary.Inputs, report.Summary.Sources, report.Summary.Usable)

	b.WriteString("## Sources\n\n")
	if len(report.Sources) == 0 {
		b.WriteString("_No source was usable; see the gaps below._\n")
	}
	for _, s := range report.Sources {
		star := ""
		if s.Recommended {
			star = " (recommended)"
		}
		fmt.Fprintf(&b, "- **%s**%s — backend `%s`, confidence `%s`, %d command(s)\n",
			s.Name, star, s.Backend, s.Confidence, s.WouldEmitCommands)
		if s.Origin != nil {
			if s.Origin.Type == "repo_url" {
				fmt.Fprintf(&b, "  - origin: `%s` @ `%s` (%s)\n", s.Origin.RepoURL, s.Origin.PinnedTag, s.Origin.RefKind)
			} else {
				fmt.Fprintf(&b, "  - origin: local_path `%s`\n", s.Origin.LocalPath)
			}
		}
	}

	var blocking, advisory []string
	add := func(g Gap, ref string) {
		line := fmt.Sprintf("- `%s` [%s %s] %s", g.Kind, g.Scope, ref, g.Message)
		if g.Blocking {
			blocking = append(blocking, line)
		} else {
			advisory = append(advisory, line)
		}
	}
	// Top-level and per-source gaps must both reach the human.
	for _, g := range report.Gaps {
		add(g, g.Ref)
	}
	for _, s := range report.Sources {
		for _, g := range s.Gaps {
			add(g, s.Name)
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
