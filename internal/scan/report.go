package scan

// version is the tool_version stamped into report.json. Kept as the only
// version marker so report.json stays otherwise deterministic (no timestamps).
const version = "0.1.0"

// Report is the machine-readable audit written to report.json and, with --json,
// to stdout. It mirrors what GAPS.md renders for humans.
type Report struct {
	SchemaVersion int            `json:"schema_version"`
	ToolVersion   string         `json:"tool_version"`
	Summary       Summary        `json:"summary"`
	Inputs        []InputReport  `json:"inputs"`
	Sources       []SourceReport `json:"sources"`
	Gaps          []Gap          `json:"gaps"`
}

// Summary is the top-level tally; ExitCode mirrors the process exit code.
type Summary struct {
	Inputs            int `json:"inputs"`
	Sources           int `json:"sources"`
	Usable            int `json:"usable"`
	PostmanCandidates int `json:"postman_candidates"`
	ExitCode          int `json:"exit_code"`
}

// InputReport records what one input produced.
type InputReport struct {
	Input      string      `json:"input"`
	Kind       string      `json:"kind,omitempty"` // git|dir|zip
	Error      string      `json:"error,omitempty"`
	Origin     *Origin     `json:"origin,omitempty"`
	Candidates []Candidate `json:"candidates,omitempty"`
	Selected   []string    `json:"selected,omitempty"`
}

// Origin records how a source's origin was resolved.
type Origin struct {
	Type      string `json:"type"` // repo_url|local_path
	RepoURL   string `json:"repo_url,omitempty"`
	PinnedTag string `json:"pinned_tag,omitempty"`
	RefKind   string `json:"ref_kind,omitempty"` // tag|sha
	LocalPath string `json:"local_path,omitempty"`
}

// Candidate is one parse attempt, usable or not.
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

// Metrics are the completeness signals behind a score and confidence.
type Metrics struct {
	Paths      int `json:"paths,omitempty"`
	Operations int `json:"operations,omitempty"`
	Schemas    int `json:"schemas,omitempty"`
}

// SourceReport mirrors one sources.yaml entry, enriched with fields Lathe's
// config cannot hold.
type SourceReport struct {
	Name              string   `json:"name"`
	Recommended       bool     `json:"recommended"`
	Level             string   `json:"level"` // L1|L2
	Extractor         string   `json:"extractor,omitempty"`
	Backend           string   `json:"backend"`
	Confidence        string   `json:"confidence"` // high|medium|low
	WouldEmitCommands int      `json:"would_emit_commands"`
	Origin            *Origin  `json:"origin,omitempty"`
	DefaultHostname   string   `json:"default_hostname,omitempty"`
	Files             []string `json:"files"`
	Metrics           *Metrics `json:"metrics,omitempty"`
	Gaps              []Gap    `json:"gaps"`
}

// Gap is something a human must confirm or fix. Kind is a closed vocabulary.
type Gap struct {
	Kind     string `json:"kind"`
	Scope    string `json:"scope"` // global|input|source|operation
	Ref      string `json:"ref,omitempty"`
	Message  string `json:"message"`
	Blocking bool   `json:"blocking"`
}

// Gap kinds (closed vocabulary).
const (
	gapAuth           = "auth"
	gapBody           = "body"
	gapResponse       = "response"
	gapDynamicRoute   = "dynamic-route"
	gapProtoNoHTTP    = "proto-no-http-annotation"
	gapGraphQLExpose  = "graphql-expose-unconfirmed"
	gapPostmanConvert = "postman-needs-conversion"
	gapAmbiguousHost  = "ambiguous-hostname"
	gapNoImmutableRef = "no-immutable-ref"
	gapParseError     = "parse-error"
	gapRefUnresolved  = "ref-closure-unresolved"
	gapBackendPending = "backend-not-yet-implemented"
)

// Confidence levels.
const (
	confHigh   = "high"
	confMedium = "medium"
	confLow    = "low"
)
