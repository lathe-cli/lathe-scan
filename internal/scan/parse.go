package scan

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Path-item keys Lathe turns into commands; parameters/$ref/servers/summary
// are not operations.
var httpMethods = map[string]bool{
	"get": true, "put": true, "post": true, "delete": true,
	"patch": true, "options": true, "head": true, "trace": true,
}

type parsed struct {
	format    string
	title     string
	metrics   Metrics
	wouldEmit int
	hostname  string
	// hostCandidates is set only when servers declare more than one host, i.e.
	// exactly when hostname is deliberately left empty.
	hostCandidates []string
	hasExtRefs     bool
	contentHash    string
	opsig          string // hash of the sorted METHOD+path set; dedups json/yaml copies
}

// Lenient like Lathe: minimal structs, unknown fields ignored. (nil, nil) when
// not a recognized Lathe-native spec.
func parseSpec(data []byte) (*parsed, error) {
	sum := sha256.Sum256(data)
	hash := "sha256:" + hex.EncodeToString(sum[:])

	var probe struct {
		OpenAPI string `yaml:"openapi"`
		Swagger string `yaml:"swagger"`
	}
	if err := yaml.Unmarshal(data, &probe); err != nil {
		return nil, fmt.Errorf("yaml/json parse: %w", err)
	}

	switch {
	case strings.HasPrefix(probe.OpenAPI, "3."):
		return parseOAS3(data, hash)
	case probe.Swagger == "2.0" || strings.HasPrefix(probe.Swagger, "2."):
		return parseSwagger2(data, hash)
	default:
		return nil, nil
	}
}

func parseOAS3(data []byte, hash string) (*parsed, error) {
	var doc struct {
		Info struct {
			Title string `yaml:"title"`
		} `yaml:"info"`
		Servers []struct {
			URL string `yaml:"url"`
		} `yaml:"servers"`
		Paths      map[string]map[string]yaml.Node `yaml:"paths"`
		Components struct {
			Schemas map[string]yaml.Node `yaml:"schemas"`
		} `yaml:"components"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("openapi3 parse: %w", err)
	}
	p := &parsed{format: "openapi3", title: doc.Info.Title, contentHash: hash}
	p.metrics.Paths = len(doc.Paths)
	p.metrics.Schemas = len(doc.Components.Schemas)
	p.metrics.Operations = countOperations(doc.Paths)
	p.opsig = operationSignature(doc.Paths)
	p.wouldEmit = p.metrics.Operations
	urls := make([]string, 0, len(doc.Servers))
	for _, s := range doc.Servers {
		urls = append(urls, s.URL)
	}
	p.hostname, p.hostCandidates = unambiguousHost(urls)
	p.hasExtRefs = hasExternalRefs(data)
	return p, nil
}

// unambiguousHost extracts default_hostname only when the servers agree on one
// host. Picking servers[0] would be the tool deciding, by list order, where
// authenticated commands are sent — and the first entry is conventionally
// production. When they disagree the field is left unset and the candidates are
// reported, so the choice stays with whoever knows which one they meant.
func unambiguousHost(urls []string) (host string, candidates []string) {
	seen := map[string]bool{}
	for _, raw := range urls {
		h := hostFromURL(raw)
		if h == "" || seen[h] {
			continue
		}
		seen[h] = true
		candidates = append(candidates, h)
	}
	if len(candidates) == 1 {
		return candidates[0], nil
	}
	return "", candidates
}

func parseSwagger2(data []byte, hash string) (*parsed, error) {
	var doc struct {
		Info struct {
			Title string `yaml:"title"`
		} `yaml:"info"`
		Host        string                          `yaml:"host"`
		Paths       map[string]map[string]yaml.Node `yaml:"paths"`
		Definitions map[string]yaml.Node            `yaml:"definitions"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("swagger parse: %w", err)
	}
	p := &parsed{format: "swagger", title: doc.Info.Title, contentHash: hash}
	p.metrics.Paths = len(doc.Paths)
	p.metrics.Schemas = len(doc.Definitions)
	p.metrics.Operations = countOperations(doc.Paths)
	p.opsig = operationSignature(doc.Paths)
	p.wouldEmit = p.metrics.Operations
	p.hostname = strings.TrimSpace(doc.Host)
	p.hasExtRefs = hasExternalRefs(data)
	return p, nil
}

// operationSignature is a stable hash of the sorted METHOD+path set. Two files
// describing the same API (e.g. swagger.json and swagger.yaml, or a copy in a
// second dir) share a signature even though their bytes differ, so dedup by
// signature collapses them while genuinely distinct monorepo APIs stay separate.
func operationSignature(paths map[string]map[string]yaml.Node) string {
	var ops []string
	for path, item := range paths {
		for method := range item {
			if httpMethods[strings.ToLower(method)] {
				ops = append(ops, strings.ToLower(method)+" "+path)
			}
		}
	}
	if len(ops) == 0 {
		return ""
	}
	sort.Strings(ops)
	sum := sha256.Sum256([]byte(strings.Join(ops, "\n")))
	return hex.EncodeToString(sum[:])
}

// Mirrors Lathe: one command per HTTP-method operation.
func countOperations(paths map[string]map[string]yaml.Node) int {
	n := 0
	for _, item := range paths {
		for method := range item {
			if httpMethods[strings.ToLower(method)] {
				n++
			}
		}
	}
	return n
}

func hostFromURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return ""
	}
	return u.Host
}

// External $refs (not "#/...") need the reference closure resolved before
// Lathe can load from a single copied file.
func hasExternalRefs(data []byte) bool {
	var root any
	if err := yaml.Unmarshal(data, &root); err != nil {
		return false
	}
	found := false
	var walk func(v any)
	walk = func(v any) {
		if found {
			return
		}
		switch t := v.(type) {
		case map[string]any:
			for k, val := range t {
				if k == "$ref" {
					if s, ok := val.(string); ok && s != "" && !strings.HasPrefix(s, "#") {
						found = true
						return
					}
				}
				walk(val)
			}
		case []any:
			for _, e := range t {
				walk(e)
			}
		}
	}
	walk(root)
	return found
}
