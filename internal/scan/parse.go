package scan

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"strings"

	"gopkg.in/yaml.v3"
)

// httpMethods are the path-item keys Lathe turns into commands. Anything else
// in a path item (parameters, $ref, servers, summary) is not an operation.
var httpMethods = map[string]bool{
	"get": true, "put": true, "post": true, "delete": true,
	"patch": true, "options": true, "head": true, "trace": true,
}

// parsed is the outcome of parsing one candidate file.
type parsed struct {
	format      string
	title       string
	metrics     Metrics
	wouldEmit   int
	hostname    string
	hasExtRefs  bool
	contentHash string
}

// parseSpec detects and parses OpenAPI 3 / Swagger 2 leniently, the same way
// Lathe does: unmarshal into minimal structs and ignore unknown fields. Returns
// (nil, "") when the file is not a recognized Lathe-native spec.
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
	p.wouldEmit = p.metrics.Operations
	if len(doc.Servers) > 0 {
		p.hostname = hostFromURL(doc.Servers[0].URL)
	}
	p.hasExtRefs = hasExternalRefs(data)
	return p, nil
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
	p.wouldEmit = p.metrics.Operations
	p.hostname = strings.TrimSpace(doc.Host)
	p.hasExtRefs = hasExternalRefs(data)
	return p, nil
}

// countOperations mirrors Lathe: one command per HTTP-method operation.
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

// hasExternalRefs reports whether any $ref points outside this document (a file
// path rather than a "#/..." local fragment). Such a spec needs its reference
// closure resolved before Lathe can load it from a single copied file.
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
