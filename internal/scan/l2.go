package scan

import (
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// L2 recovers routes from source when no usable native spec exists.
// Never executes, imports, builds, or installs anything.

const (
	l2MaxFiles  = 4000
	l2DraftName = "openapi.yaml"
)

type route struct {
	method string
	path   string
	file   string // repo-relative file the route was recovered from
}

// detect gates finders so frameworks that share a call shape (Gin vs Echo)
// are disambiguated by import markers, not mislabeled.
type extractor struct {
	name   string
	exts   map[string]bool
	detect func(src string) bool
	find   func(src string) []route
}

var jsExts = map[string]bool{".ts": true, ".js": true, ".mjs": true, ".cjs": true}

// Fixed priority; most routes wins ties.
var extractors = []extractor{
	{name: "fastapi", exts: map[string]bool{".py": true}, detect: detectFastAPI, find: findFastAPI},
	{name: "flask", exts: map[string]bool{".py": true}, detect: detectFlask, find: findFlask},
	{name: "django", exts: map[string]bool{".py": true}, detect: detectDjango, find: findDjango},
	{name: "spring", exts: map[string]bool{".java": true}, detect: detectSpring, find: findSpring},
	{name: "nestjs", exts: jsExts, detect: detectNest, find: findNest},
	{name: "express", exts: jsExts, detect: markerDetect("express"), find: findLowerVerb},
	{name: "fastify", exts: jsExts, detect: markerDetect("fastify"), find: findLowerVerb},
	{name: "gin", exts: map[string]bool{".go": true}, detect: markerDetect("gin-gonic/gin"), find: findColonVerb},
	{name: "echo", exts: map[string]bool{".go": true}, detect: markerDetect("labstack/echo"), find: findColonVerb},
	{name: "chi", exts: map[string]bool{".go": true}, detect: markerDetect("go-chi/chi"), find: findChi},
	{name: "rails", exts: map[string]bool{".rb": true}, detect: detectRails, find: findRails},
	{name: "laravel", exts: map[string]bool{".php": true}, detect: detectLaravel, find: findLaravel},
	{name: "aspnet", exts: map[string]bool{".cs": true}, detect: detectAspNet, find: findAspNet},
	{name: "ktor", exts: map[string]bool{".kt": true}, detect: markerDetect("io.ktor"), find: findKtor},
	{name: "actix", exts: map[string]bool{".rs": true}, detect: markerDetect("actix"), find: findActix},
	{name: "axum", exts: map[string]bool{".rs": true}, detect: markerDetect("axum"), find: findAxum},
}

var (
	reFastAPI = regexp.MustCompile(`@\s*\w+\.(get|post|put|delete|patch|options|head)\(\s*["']([^"']+)["']`)
	// Path must start with "/" to avoid false positives on non-route calls.
	reGoVerb        = regexp.MustCompile(`\.(GET|POST|PUT|DELETE|PATCH|OPTIONS|HEAD)\(\s*"(/[^"]*)"`)
	reChi           = regexp.MustCompile(`\.(Get|Post|Put|Delete|Patch|Options|Head)\(\s*"(/[^"]*)"`)
	reSpringVerbMap = regexp.MustCompile(`@(Get|Post|Put|Delete|Patch)Mapping\s*\(\s*(?:value\s*=\s*|path\s*=\s*)?"([^"]+)"`)
	// @RequestMapping needs an explicit RequestMethod; bare class-level prefixes are skipped.
	reSpringReqMap    = regexp.MustCompile(`@RequestMapping\s*\(([^)]*)\)`)
	reSpringReqMethod = regexp.MustCompile(`RequestMethod\.(GET|POST|PUT|DELETE|PATCH|OPTIONS|HEAD)`)
	reSpringReqPath   = regexp.MustCompile(`(?:value|path)\s*=\s*"([^"]+)"|"([^"]+)"`)
	// Router-ish receivers only so axios.get / db.get are not routes.
	reLowerVerb    = regexp.MustCompile(`\b(?:app|router|route|api|server|fastify|r)\.(get|post|put|delete|patch|options|head)\(\s*['"](/[^'"]*)['"]`)
	reNest         = regexp.MustCompile(`@(Get|Post|Put|Delete|Patch|Options|Head)\(\s*['"]([^'"]+)['"]\s*\)`)
	reFlask        = regexp.MustCompile(`@\w+\.route\(\s*['"]([^'"]+)['"]([^)]*)\)`)
	reFlaskMethods = regexp.MustCompile(`methods\s*=\s*\[([^\]]*)\]`)
	reFlaskMethod  = regexp.MustCompile(`['"](GET|POST|PUT|DELETE|PATCH|OPTIONS|HEAD)['"]`)
	reDjango       = regexp.MustCompile(`\b(?:path|re_path|url)\(\s*[r]?['"]([^'"]+)['"]`)
	reDjangoConv   = regexp.MustCompile(`<(?:\w+:)?(\w+)>`)
	reDjangoNamed  = regexp.MustCompile(`\(\?P<(\w+)>[^)]*\)`)
	reRails        = regexp.MustCompile(`\b(get|post|put|patch|delete)\s+['"]([^'"]+)['"]`)
	reLaravel      = regexp.MustCompile(`Route::(get|post|put|patch|delete)\(\s*['"]([^'"]+)['"]`)
	reAspNet       = regexp.MustCompile(`\[Http(Get|Post|Put|Delete|Patch)\(\s*"([^"]+)"`)
	// Exclude method-call receivers (map.get).
	reKtor  = regexp.MustCompile(`[^.\w](get|post|put|delete|patch)\(\s*"([^"]*)"`)
	reActix = regexp.MustCompile(`#\[(get|post|put|delete|patch)\(\s*"([^"]+)"\s*\)\]`)
	reAxum  = regexp.MustCompile(`\.route\(\s*"([^"]+)"\s*,\s*(get|post|put|delete|patch)\b`)
)

type sourceFile struct {
	rel  string
	ext  string
	body string
}

// scanRoot is the input tree, so x-lathe-source-file is repo-relative and means
// the same thing to a reviewer as a path in report.json does.
func runL2(idx *fileIndex, input, scanRoot string) (*builtSource, *Candidate) {
	files := make([]sourceFile, 0, len(idx.sources))
	for _, f := range idx.sources {
		data, err := readWithin(scanRoot, f)
		if err != nil {
			continue
		}
		files = append(files, sourceFile{
			rel:  repoRelativePath(scanRoot, f),
			ext:  strings.ToLower(filepath.Ext(f)),
			body: string(data),
		})
	}

	// Detection is whole-corpus (an import marker can sit in any file), but
	// extraction is per file: it keeps each route's origin and stops a regexp
	// from matching across a file boundary into a route that exists nowhere.
	best := extractor{}
	var bestRoutes []route
	for _, ex := range extractors {
		if !detects(ex, files) {
			continue
		}
		var routes []route
		for _, f := range files {
			if !ex.exts[f.ext] {
				continue
			}
			for _, r := range ex.find(f.body) {
				r.file = f.rel
				routes = append(routes, r)
			}
		}
		routes = dedupeRoutes(routes)
		if len(routes) > len(bestRoutes) {
			best, bestRoutes = ex, routes
		}
	}
	if len(bestRoutes) == 0 {
		return nil, nil
	}

	// Derive the name from the original input, not inputAbs — for a zip, inputAbs
	// is a random extraction dir, which would make names nondeterministic and
	// break --merge.
	name := firstNonEmpty(sanitizeName(strings.TrimSuffix(filepath.Base(input), filepath.Ext(input))), "api")
	draft := synthesizeOpenAPI(name, best.name, bestRoutes)
	inputFileSet := map[string]bool{}
	for _, route := range bestRoutes {
		inputFileSet[route.file] = true
	}
	inputFiles := make([]string, 0, len(inputFileSet))
	for file := range inputFileSet {
		inputFiles = append(inputFiles, file)
	}
	sort.Strings(inputFiles)

	b := &builtSource{
		baseName:   name,
		identity:   "l2",
		origin:     &Origin{Type: "local_path"},
		yc:         &ycSource{Backend: "openapi3", OpenAPI3: &filesBlock{Files: []string{l2DraftName}}},
		synth:      []synthFile{{relTo: l2DraftName, content: draft}},
		inputRoot:  scanRoot,
		inputFiles: inputFiles,
	}
	b.report = &SourceReport{
		Level: "L2", Extractor: best.name, Backend: "openapi3",
		WouldEmitCommands: len(bestRoutes),
		Files:             []string{l2DraftName},
		Metrics:           &Metrics{Paths: countPaths(bestRoutes), Operations: len(bestRoutes)},
		Confidence:        confMedium,
		Gaps: []Gap{
			{Kind: gapBody, Scope: "source", Message: "request bodies were not recovered from source", Blocking: false},
			{Kind: gapResponse, Scope: "source", Message: "response shapes were not recovered from source", Blocking: false},
			{Kind: gapAuth, Scope: "source", Message: "auth requirements were not recovered from source", Blocking: false},
			{Kind: gapDynamicRoute, Scope: "source", Message: "prefixes from route groups or class-level mappings may not be applied; verify full paths", Blocking: false},
		},
	}
	if best.name == "django" {
		b.report.Gaps = append(b.report.Gaps, Gap{
			Kind: gapMethodUnverified, Scope: "source",
			Message: "Django urlconf does not declare HTTP methods; every route was assumed GET — verify each view", Blocking: false,
		})
	}
	// A silent cap reads as "we looked at everything"; say what was left out.
	if idx.truncated {
		b.report.Gaps = append(b.report.Gaps, Gap{
			Kind: gapScanTruncated, Scope: "source",
			Message:  fmt.Sprintf("only the first %d source files were analyzed; routes defined beyond them are missing", l2MaxFiles),
			Blocking: false,
		})
	}
	cand := &Candidate{
		Path: input, Format: "openapi3", Parsed: true,
		Metrics: b.report.Metrics,
		Reason:  fmt.Sprintf("L2 %s extractor, %d routes", best.name, len(bestRoutes)),
	}
	return b, cand
}

// A marker in any claimed file gates the whole extractor: Gin vs Echo share a
// call shape and are told apart only by their import.
func detects(ex extractor, files []sourceFile) bool {
	if ex.detect == nil {
		return true
	}
	for _, f := range files {
		if ex.exts[f.ext] && ex.detect(f.body) {
			return true
		}
	}
	return false
}

func detectFastAPI(src string) bool {
	return strings.Contains(src, "fastapi") || strings.Contains(src, "FastAPI") || strings.Contains(src, "APIRouter")
}

func detectSpring(src string) bool {
	return strings.Contains(src, "springframework") ||
		strings.Contains(src, "Mapping")
}

func detectNest(src string) bool {
	return strings.Contains(src, "@nestjs") || strings.Contains(src, "@Controller")
}

func detectFlask(src string) bool {
	return strings.Contains(src, "flask") || strings.Contains(src, "Flask")
}

func detectDjango(src string) bool {
	return strings.Contains(src, "django") || strings.Contains(src, "urlpatterns")
}

// Default GET when methods omitted (Flask semantics).
func findFlask(src string) []route {
	var out []route
	for _, m := range reFlask.FindAllStringSubmatch(src, -1) {
		path := flaskPath(m[1])
		methods := []string{"GET"}
		if ml := reFlaskMethods.FindStringSubmatch(m[2]); ml != nil {
			methods = methods[:0]
			for _, mm := range reFlaskMethod.FindAllStringSubmatch(ml[1], -1) {
				methods = append(methods, strings.ToUpper(mm[1]))
			}
		}
		for _, method := range methods {
			out = append(out, route{method: method, path: path})
		}
	}
	return out
}

// Django urlconf has no HTTP methods → assume GET; runL2 adds method-unverified gap.
func findDjango(src string) []route {
	var out []route
	for _, m := range reDjango.FindAllStringSubmatch(src, -1) {
		out = append(out, route{method: "GET", path: djangoPath(m[1])})
	}
	return out
}

func flaskPath(p string) string {
	return normalizePath(reDjangoConv.ReplaceAllString(p, "{$1}"))
}

func djangoPath(p string) string {
	p = strings.TrimPrefix(p, "^")
	p = strings.TrimSuffix(p, "$")
	p = reDjangoNamed.ReplaceAllString(p, "{$1}")
	p = reDjangoConv.ReplaceAllString(p, "{$1}")
	return normalizePath(p)
}

func markerDetect(marker string) func(string) bool {
	return func(src string) bool { return strings.Contains(src, marker) }
}

func findLowerVerb(src string) []route {
	var out []route
	for _, m := range reLowerVerb.FindAllStringSubmatch(src, -1) {
		out = append(out, route{method: strings.ToUpper(m[1]), path: colonPath(m[2])})
	}
	return out
}

// Controller prefixes are not applied (reported as dynamic-route gap).
func findNest(src string) []route {
	var out []route
	for _, m := range reNest.FindAllStringSubmatch(src, -1) {
		out = append(out, route{method: strings.ToUpper(m[1]), path: colonPath(m[2])})
	}
	return out
}

func findFastAPI(src string) []route {
	var out []route
	for _, m := range reFastAPI.FindAllStringSubmatch(src, -1) {
		out = append(out, route{method: strings.ToUpper(m[1]), path: normalizePath(m[2])})
	}
	return out
}

func findColonVerb(src string) []route {
	var out []route
	for _, m := range reGoVerb.FindAllStringSubmatch(src, -1) {
		out = append(out, route{method: strings.ToUpper(m[1]), path: colonPath(m[2])})
	}
	return out
}

func findChi(src string) []route {
	var out []route
	for _, m := range reChi.FindAllStringSubmatch(src, -1) {
		out = append(out, route{method: strings.ToUpper(m[1]), path: normalizePath(m[2])})
	}
	return out
}

// Bare @RequestMapping (class-level prefix, no method) is skipped, not guessed.
func findSpring(src string) []route {
	var out []route
	for _, m := range reSpringVerbMap.FindAllStringSubmatch(src, -1) {
		out = append(out, route{method: strings.ToUpper(m[1]), path: normalizePath(m[2])})
	}
	for _, m := range reSpringReqMap.FindAllStringSubmatch(src, -1) {
		args := m[1]
		verb := reSpringReqMethod.FindStringSubmatch(args)
		if verb == nil {
			continue // class-level prefix or method-agnostic mapping
		}
		path := reSpringReqPath.FindStringSubmatch(args)
		if path == nil {
			continue
		}
		p := firstNonEmpty(path[1], path[2])
		out = append(out, route{method: strings.ToUpper(verb[1]), path: normalizePath(p)})
	}
	return out
}

func detectRails(src string) bool {
	return strings.Contains(src, "Rails") || strings.Contains(src, "ActionController") || strings.Contains(src, "routes.draw")
}

func detectLaravel(src string) bool {
	return strings.Contains(src, "Illuminate") || strings.Contains(src, "Route::")
}

func detectAspNet(src string) bool {
	return strings.Contains(src, "Microsoft.AspNetCore") || strings.Contains(src, "[ApiController]") || strings.Contains(src, "Microsoft.AspNet")
}

func findRails(src string) []route {
	var out []route
	for _, m := range reRails.FindAllStringSubmatch(src, -1) {
		out = append(out, route{method: strings.ToUpper(m[1]), path: colonPath(m[2])})
	}
	return out
}

func findLaravel(src string) []route {
	var out []route
	for _, m := range reLaravel.FindAllStringSubmatch(src, -1) {
		out = append(out, route{method: strings.ToUpper(m[1]), path: laravelPath(m[2])})
	}
	return out
}

func findAspNet(src string) []route {
	var out []route
	for _, m := range reAspNet.FindAllStringSubmatch(src, -1) {
		out = append(out, route{method: strings.ToUpper(m[1]), path: normalizePath(m[2])})
	}
	return out
}

func findKtor(src string) []route {
	var out []route
	for _, m := range reKtor.FindAllStringSubmatch(src, -1) {
		out = append(out, route{method: strings.ToUpper(m[1]), path: normalizePath(m[2])})
	}
	return out
}

func findActix(src string) []route {
	var out []route
	for _, m := range reActix.FindAllStringSubmatch(src, -1) {
		out = append(out, route{method: strings.ToUpper(m[1]), path: normalizePath(m[2])})
	}
	return out
}

// Axum is path-first, method-second (.route("/path", get(...))).
func findAxum(src string) []route {
	var out []route
	for _, m := range reAxum.FindAllStringSubmatch(src, -1) {
		out = append(out, route{method: strings.ToUpper(m[2]), path: normalizePath(m[1])})
	}
	return out
}

func laravelPath(p string) string {
	return normalizePath(strings.ReplaceAll(p, "?}", "}"))
}

func colonPath(p string) string {
	segs := strings.Split(p, "/")
	for i, s := range segs {
		if strings.HasPrefix(s, ":") || strings.HasPrefix(s, "*") {
			segs[i] = "{" + s[1:] + "}"
		}
	}
	return normalizePath(strings.Join(segs, "/"))
}

func normalizePath(p string) string {
	if p == "" {
		return "/"
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return p
}

func dedupeRoutes(routes []route) []route {
	seen := map[string]bool{}
	var out []route
	for _, r := range routes {
		key := r.method + " " + r.path
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, r)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].path != out[j].path {
			return out[i].path < out[j].path
		}
		return out[i].method < out[j].method
	})
	return out
}

func countPaths(routes []route) int {
	paths := map[string]bool{}
	for _, r := range routes {
		paths[r.path] = true
	}
	return len(paths)
}

var rePathParam = regexp.MustCompile(`\{([^}]+)\}`)

func synthesizeOpenAPI(title, extractor string, routes []route) []byte {
	paths := map[string]any{}
	usedIDs := map[string]bool{}
	for _, r := range routes {
		item, ok := paths[r.path].(map[string]any)
		if !ok {
			item = map[string]any{}
			paths[r.path] = item
		}
		op := map[string]any{
			// Lathe drops operations without an operationId and aborts codegen on
			// colliding command names, so each synthesized id must be present and
			// unique (paths like /groups and /Groups normalize to the same base).
			"operationId":        uniqueOpID(operationID(r.method, r.path), usedIDs),
			"x-lathe-confidence": "medium",
			"x-lathe-gaps":       []any{"body", "response", "auth"},
			"responses": map[string]any{
				"200": map[string]any{"description": "extracted by lathe-scan; response shape unknown"},
			},
		}
		// The draft exists to be reviewed; a route with no origin cannot be checked.
		if r.file != "" {
			op["x-lathe-source-file"] = r.file
		}
		if params := pathParams(r.path); len(params) > 0 {
			op["parameters"] = params
		}
		item[strings.ToLower(r.method)] = op
	}

	doc := map[string]any{
		"openapi": "3.0.3",
		"info": map[string]any{
			"title":   title + " (extracted)",
			"version": "0.0.0",
			"x-lathe-scan": map[string]any{
				"extractor":  extractor,
				"confidence": "medium",
			},
		},
		"paths": paths,
	}
	data, err := yaml.Marshal(doc)
	if err != nil {
		return []byte("openapi: 3.0.3\n")
	}
	return data
}

// operationID builds a unique camelCase id like "getUsersId" from a method and
// path. Lathe derives the command name from it (camel → kebab), so a synth op
// without one would be dropped at codegen.
func operationID(method, path string) string {
	var b strings.Builder
	b.WriteString(strings.ToLower(method))
	for _, seg := range strings.Split(path, "/") {
		s := alnumOnly(strings.Trim(seg, "{}"))
		if s == "" {
			continue
		}
		b.WriteString(strings.ToUpper(s[:1]))
		b.WriteString(s[1:])
	}
	return b.String()
}

// uniqueOpID keeps a synthesized operationId unique within one spec so Lathe's
// command-name derivation cannot collide.
func uniqueOpID(base string, used map[string]bool) string {
	id := base
	for i := 2; used[id]; i++ {
		id = fmt.Sprintf("%s%d", base, i)
	}
	used[id] = true
	return id
}

func alnumOnly(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func pathParams(path string) []any {
	var out []any
	for _, m := range rePathParam.FindAllStringSubmatch(path, -1) {
		out = append(out, map[string]any{
			"name":     m[1],
			"in":       "path",
			"required": true,
			"schema":   map[string]any{"type": "string"},
		})
	}
	return out
}

func isSourceFile(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".py", ".go", ".java", ".ts", ".js", ".mjs", ".cjs",
		".rb", ".php", ".cs", ".kt", ".rs":
		return true
	}
	return false
}
