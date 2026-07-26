package scan

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
)

// Postman is not Lathe-native: convert each collection to a synthesized OpenAPI 3
// draft (always local_path, medium confidence), same shape as L2.

func postmanFiles(idx *fileIndex, root string) []string {
	var out []string
	for _, f := range idx.jsons {
		data, err := readWithin(root, f)
		if err != nil {
			continue
		}
		if isPostmanCollection(data) {
			out = append(out, f)
		}
	}
	return out
}

func isJSONFile(path string) bool {
	return strings.EqualFold(filepath.Ext(path), ".json")
}

func isPostmanCollection(data []byte) bool {
	s := string(data)
	return strings.Contains(s, "schema.getpostman.com") || strings.Contains(s, "_postman_id")
}

type pmCollection struct {
	Info struct {
		Name string `json:"name"`
	} `json:"info"`
	Item []pmItem `json:"item"`
}

type pmItem struct {
	Name string   `json:"name"`
	Item []pmItem `json:"item"`
	// Request is a string (URL shorthand) or an object; kept raw so a string-form
	// item does not fail the whole collection's unmarshal.
	Request json.RawMessage `json:"request"`
}

type pmRequest struct {
	Method string          `json:"method"`
	URL    json.RawMessage `json:"url"`
}

func buildPostmanSources(files []string, root string) ([]*builtSource, []Candidate) {
	var sources []*builtSource
	var cands []Candidate
	for _, f := range files {
		data, err := readWithin(root, f)
		if err != nil {
			continue
		}
		rel := relOrBase(root, f)
		var col pmCollection
		if err := json.Unmarshal(data, &col); err != nil {
			cands = append(cands, Candidate{Path: rel, Format: "postman", Parsed: false, Error: err.Error()})
			continue
		}
		routes := collectPostmanRoutes(col.Item)
		for i := range routes {
			routes[i].file = rel
		}
		routes = dedupeRoutes(routes)
		cand := Candidate{
			Path: rel, Format: "postman", Parsed: true,
			Metrics: &Metrics{Operations: len(routes)},
			Reason:  fmt.Sprintf("postman collection, %d requests", len(routes)),
		}
		cands = append(cands, cand)
		if len(routes) == 0 {
			continue
		}

		name := firstNonEmpty(sanitizeName(col.Info.Name), sanitizeName(parentDirName(rel)), "postman")
		draft := synthesizeOpenAPI(name, "postman", routes)
		b := &builtSource{
			baseName: name,
			identity: rel,
			origin:   &Origin{Type: "local_path"},
			yc:       &ycSource{Backend: "openapi3", OpenAPI3: &filesBlock{Files: []string{l2DraftName}}},
			synth:    []synthFile{{relTo: l2DraftName, content: draft}},
			report: &SourceReport{
				Level: "L1", Extractor: "postman", Backend: "openapi3",
				WouldEmitCommands: len(routes),
				Files:             []string{l2DraftName},
				Metrics:           &Metrics{Paths: countPaths(routes), Operations: len(routes)},
				Confidence:        confMedium,
				Gaps: []Gap{
					{Kind: gapPostmanConvert, Scope: "source", Message: "converted from a Postman collection; request/response schemas are approximate", Blocking: false},
					{Kind: gapResponse, Scope: "source", Message: "response shapes were not recovered", Blocking: false},
				},
			},
		}
		sources = append(sources, b)
	}
	return sources, cands
}

func collectPostmanRoutes(items []pmItem) []route {
	var out []route
	for _, it := range items {
		if len(it.Item) > 0 {
			out = append(out, collectPostmanRoutes(it.Item)...)
		}
		method, url := parsePostmanRequest(it.Request)
		if method == "" {
			continue
		}
		path := postmanURLPath(url)
		if path == "" {
			continue
		}
		out = append(out, route{method: method, path: path})
	}
	return out
}

// parsePostmanRequest handles a request that is either an object ({method, url})
// or a bare URL string (method defaults to GET). Returns "" method to skip.
func parsePostmanRequest(raw json.RawMessage) (method string, url json.RawMessage) {
	if len(raw) == 0 {
		return "", nil
	}
	var obj pmRequest
	if err := json.Unmarshal(raw, &obj); err == nil && obj.Method != "" {
		return strings.ToUpper(obj.Method), obj.URL
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil && s != "" {
		return "GET", raw // string-form request: the whole value is the URL
	}
	return "", nil
}

// ":id"/"{{var}}" → "{id}"/"{var}".
func postmanURLPath(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var obj struct {
		Path []string `json:"path"`
		Raw  string   `json:"raw"`
	}
	if err := json.Unmarshal(raw, &obj); err == nil && (len(obj.Path) > 0 || obj.Raw != "") {
		if len(obj.Path) > 0 {
			return normalizePath(strings.Join(mapSegments(obj.Path), "/"))
		}
		return rawURLPath(obj.Raw)
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return rawURLPath(s)
	}
	return ""
}

func rawURLPath(raw string) string {
	raw = strings.TrimSpace(raw)
	if i := strings.Index(raw, "://"); i >= 0 {
		raw = raw[i+3:]
		if slash := strings.IndexByte(raw, '/'); slash >= 0 {
			raw = raw[slash:]
		} else {
			raw = "/"
		}
	}
	if q := strings.IndexByte(raw, '?'); q >= 0 {
		raw = raw[:q]
	}
	segs := strings.Split(strings.TrimPrefix(raw, "/"), "/")
	return normalizePath(strings.Join(mapSegments(segs), "/"))
}

func mapSegments(segs []string) []string {
	out := make([]string, 0, len(segs))
	for _, s := range segs {
		switch {
		case strings.HasPrefix(s, ":"):
			out = append(out, "{"+s[1:]+"}")
		case strings.HasPrefix(s, "{{") && strings.HasSuffix(s, "}}"):
			out = append(out, "{"+strings.TrimSuffix(strings.TrimPrefix(s, "{{"), "}}")+"}")
		default:
			out = append(out, s)
		}
	}
	return out
}
