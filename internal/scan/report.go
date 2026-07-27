package scan

// version is the only version marker in report.json so output stays
// deterministic (no timestamps). Overridden at build time via
// -ldflags "-X github.com/lathe-cli/lathe-scan/internal/scan.version=...".
var version = "0.1.0"

// Version is what the binary reports and what lands in report.tool_version.
func Version() string { return version }

type Report struct {
	SchemaVersion int            `json:"schema_version"`
	ToolVersion   string         `json:"tool_version"`
	Summary       Summary        `json:"summary"`
	Inputs        []InputReport  `json:"inputs"`
	Sources       []SourceReport `json:"sources"`
	// Preserved lists manifest entries --merge carried over rather than produced.
	// Sources keeps every usable candidate; only recommended candidates are emitted.
	Preserved []PreservedSource `json:"preserved,omitempty"`
	Gaps      []Gap             `json:"gaps"`
}

// PreservedSource carries provenance so ownership survives any number of
// merges: after scanning A then B, A appears only here, and a later re-scan of A
// must still recognize the entry as its own.
type PreservedSource struct {
	Name       string      `json:"name"`
	Provenance *Provenance `json:"provenance,omitempty"` // absent for foreign, hand-written entries
}

type Summary struct {
	Inputs            int `json:"inputs"`
	Sources           int `json:"sources"`
	Usable            int `json:"usable"`
	PostmanCandidates int `json:"postman_candidates"`
	ExitCode          int `json:"exit_code"`
}

type InputReport struct {
	Input      string      `json:"input"`
	Kind       string      `json:"kind,omitempty"` // git|dir|zip
	Error      string      `json:"error,omitempty"`
	Origin     *Origin     `json:"origin,omitempty"`
	Candidates []Candidate `json:"candidates,omitempty"`
	Selected   []string    `json:"selected,omitempty"`
}

// Provenance ties a sources.yaml entry back to the scan that produced it so
// --merge can update it in place. Stable across runs and independent of the
// source's assigned name.
type Provenance struct {
	Input   string `json:"input"`   // absolute input path
	Backend string `json:"backend"` // openapi3|swagger|proto|graphql
	Key     string `json:"key"`     // where the source sits in that input (spec path, proto root, …)
}

type Origin struct {
	Type      string `json:"type"` // repo_url|local_path
	RepoURL   string `json:"repo_url,omitempty"`
	PinnedTag string `json:"pinned_tag,omitempty"`
	RefKind   string `json:"ref_kind,omitempty"` // tag|sha
	LocalPath string `json:"local_path,omitempty"`
}

type Candidate struct {
	Path        string   `json:"path"`
	Format      string   `json:"format"` // openapi3|swagger|proto|graphql|postman
	Parsed      bool     `json:"parsed"`
	Error       string   `json:"error,omitempty"`
	ContentHash string   `json:"content_hash,omitempty"`
	DuplicateOf string   `json:"duplicate_of,omitempty"`
	Score       int      `json:"score"`
	Metrics     *Metrics `json:"metrics,omitempty"`
	Reason      string   `json:"reason,omitempty"`
}

type Metrics struct {
	Paths      int `json:"paths,omitempty"`
	Operations int `json:"operations,omitempty"`
	Schemas    int `json:"schemas,omitempty"`
}

// SourceReport mirrors one sources.yaml entry, plus fields Lathe's config
// cannot hold.
type SourceReport struct {
	Name              string      `json:"name"`
	Recommended       bool        `json:"recommended"`
	Level             string      `json:"level"` // L1|L2
	Extractor         string      `json:"extractor,omitempty"`
	Backend           string      `json:"backend"`
	Confidence        string      `json:"confidence"` // high|medium|low
	WouldEmitCommands int         `json:"would_emit_commands"`
	Origin            *Origin     `json:"origin,omitempty"`
	Input             string      `json:"input,omitempty"`
	Provenance        *Provenance `json:"provenance,omitempty"`
	DefaultHostname   string      `json:"default_hostname,omitempty"`
	Files             []string    `json:"files"`
	Metrics           *Metrics    `json:"metrics,omitempty"`
	Gaps              []Gap       `json:"gaps"`
}

type Gap struct {
	Kind     string `json:"kind"`
	Scope    string `json:"scope"` // global|input|source|operation
	Ref      string `json:"ref,omitempty"`
	Message  string `json:"message"`
	Blocking bool   `json:"blocking"`
}

const (
	gapAuth             = "auth"
	gapBody             = "body"
	gapResponse         = "response"
	gapDynamicRoute     = "dynamic-route"
	gapProtoNoHTTP      = "proto-no-http-annotation"
	gapGraphQLExpose    = "graphql-expose-unconfirmed"
	gapPostmanConvert   = "postman-needs-conversion"
	gapAmbiguousHost    = "ambiguous-hostname"
	gapNoImmutableRef   = "no-immutable-ref"
	gapOriginNotAtRef   = "origin-file-not-at-ref"
	gapParseError       = "parse-error"
	gapRefUnresolved    = "ref-closure-unresolved"
	gapGraphQLSplit     = "graphql-split-schema"
	gapProtoImports     = "proto-imports-unverified"
	gapMethodUnverified = "http-method-unverified"
	gapRefBundled       = "ref-closure-bundled"
	gapScanTruncated    = "scan-truncated"
	gapInputError       = "input-error"
	gapExposePreserved  = "graphql-expose-preserved"
	gapExposeStale      = "graphql-expose-stale"
	gapExposeEmpty      = "graphql-expose-empty"
	gapExposeUnreadable = "graphql-expose-unreadable"
	gapSourceKept       = "source-kept"
)

const (
	confHigh   = "high"
	confMedium = "medium"
	confLow    = "low"
)
