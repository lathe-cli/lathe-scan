package scan

import (
	"path/filepath"
	"testing"
)

const fastAPISrc = `from fastapi import FastAPI, APIRouter
app = FastAPI()
router = APIRouter()

@app.get("/health")
def health(): ...

@router.get("/users/{user_id}")
def get_user(user_id: int): ...

@router.post("/users")
def create_user(): ...
`

const ginSrc = `package main
import "github.com/gin-gonic/gin"
func setup(r *gin.Engine) {
	r.GET("/ping", handlePing)
	r.GET("/items/:id", getItem)
	r.PUT("/items/:id", putItem)
}
`

func TestFindFastAPI(t *testing.T) {
	routes := dedupeRoutes(findFastAPI(fastAPISrc))
	want := []route{
		{"POST", "/users"},
		{"GET", "/health"},
		{"GET", "/users/{user_id}"},
	}
	if len(routes) != len(want) {
		t.Fatalf("got %d routes, want %d: %+v", len(routes), len(want), routes)
	}
	seen := map[string]bool{}
	for _, r := range routes {
		seen[r.method+" "+r.path] = true
	}
	for _, w := range want {
		if !seen[w.method+" "+w.path] {
			t.Errorf("missing route %s %s", w.method, w.path)
		}
	}
}

func TestFindGinConvertsParams(t *testing.T) {
	routes := dedupeRoutes(findColonVerb(ginSrc))
	if len(routes) != 3 {
		t.Fatalf("got %d routes, want 3: %+v", len(routes), routes)
	}
	for _, r := range routes {
		if r.path == "/items/:id" {
			t.Errorf("gin param not converted: %s", r.path)
		}
	}
}

func TestColonPath(t *testing.T) {
	cases := map[string]string{
		"/items/:id":   "/items/{id}",
		"/files/*path": "/files/{path}",
		"users":        "/users",
		"/a/:x/b/:y":   "/a/{x}/b/{y}",
	}
	for in, want := range cases {
		if got := colonPath(in); got != want {
			t.Errorf("colonPath(%q) = %q, want %q", in, got, want)
		}
	}
}

const springSrc = `package com.acme;
import org.springframework.web.bind.annotation.*;

@RestController
@RequestMapping("/api")
public class UserController {
  @GetMapping("/users/{id}")
  public User get(Long id) { return null; }

  @PostMapping(value = "/users")
  public User create(User u) { return u; }

  @RequestMapping(value = "/legacy", method = RequestMethod.DELETE)
  public void legacy() {}
}
`

const echoSrc = `package main
import "github.com/labstack/echo/v4"
func main() {
	e := echo.New()
	e.GET("/health", h)
	e.GET("/books/:isbn", h)
	e.POST("/books", h)
}
`

const chiSrc = `package main
import "github.com/go-chi/chi/v5"
func routes() {
	r := chi.NewRouter()
	r.Get("/tasks/{taskID}", h)
	r.Post("/tasks", h)
	cache.Get("ignored")
}
`

func TestFindSpring(t *testing.T) {
	routes := dedupeRoutes(findSpring(springSrc))
	// Class-level @RequestMapping("/api") has no method -> skipped, not a route.
	want := map[string]bool{"GET /users/{id}": true, "POST /users": true, "DELETE /legacy": true}
	if len(routes) != len(want) {
		t.Fatalf("got %d routes, want %d: %+v", len(routes), len(want), routes)
	}
	for _, r := range routes {
		if !want[r.method+" "+r.path] {
			t.Errorf("unexpected route %s %s", r.method, r.path)
		}
	}
}

func TestExecuteL2Echo(t *testing.T) {
	in := inputDir(t, "server.go", echoSrc)
	out := t.TempDir()
	if err := Execute(Options{Inputs: []string{in}, Out: out}); err != nil {
		t.Fatal(err)
	}
	rep := readReport(t, out).Sources[0]
	// Echo and Gin share the call shape; the import marker must label it echo.
	if rep.Extractor != "echo" {
		t.Errorf("extractor = %q, want echo", rep.Extractor)
	}
	if rep.WouldEmitCommands != 3 {
		t.Errorf("wouldEmit = %d, want 3", rep.WouldEmitCommands)
	}
}

func TestChiIgnoresNonRouteCalls(t *testing.T) {
	routes := dedupeRoutes(findChi(chiSrc))
	// cache.Get("ignored") must not become a route (path lacks a leading slash).
	if len(routes) != 2 {
		t.Fatalf("got %d routes, want 2: %+v", len(routes), routes)
	}
	for _, r := range routes {
		if r.path == "/ignored" || !hasPrefix(r.path, "/tasks") {
			t.Errorf("unexpected chi route: %+v", r)
		}
	}
}

func TestGinNotDetectedWithoutMarker(t *testing.T) {
	// Verb calls but no framework import marker -> no Go extraction (conservative).
	in := inputDir(t, "x.go", "package main\nfunc f(r X){ r.GET(\"/a\", h) }\n")
	out := t.TempDir()
	err := Execute(Options{Inputs: []string{in}, Out: out})
	var noSrc ErrNoSources
	if err == nil || !asNoSources(err, &noSrc) {
		t.Errorf("expected no sources without a framework marker, got %v", err)
	}
}

func TestFindNest(t *testing.T) {
	src := "import { Controller, Get, Post } from '@nestjs/common';\n@Get(':id') a(){}\n@Post('/') b(){}\n@Get() c(){}"
	routes := dedupeRoutes(findNest(src))
	// @Get() with no path literal is skipped; :id -> {id}.
	if len(routes) != 2 {
		t.Fatalf("got %d routes, want 2: %+v", len(routes), routes)
	}
}

func TestFindLowerVerbIgnoresClients(t *testing.T) {
	src := `const app = express();
app.get('/users/:id', h);
app.post('/users', h);
axios.get('/nope');
db.get('/nope2');`
	routes := dedupeRoutes(findLowerVerb(src))
	if len(routes) != 2 {
		t.Fatalf("got %d routes, want 2 (client calls excluded): %+v", len(routes), routes)
	}
	for _, r := range routes {
		if r.path == "/nope" || r.path == "/nope2" {
			t.Errorf("client call became a route: %+v", r)
		}
	}
}

func TestFindFlaskExpandsMethods(t *testing.T) {
	src := `@app.route("/users/<int:id>", methods=["GET", "DELETE"])
def u(): ...
@app.route("/health")
def h(): ...`
	routes := dedupeRoutes(findFlask(src))
	want := map[string]bool{"GET /users/{id}": true, "DELETE /users/{id}": true, "GET /health": true}
	if len(routes) != len(want) {
		t.Fatalf("got %d routes, want %d: %+v", len(routes), len(want), routes)
	}
	for _, r := range routes {
		if !want[r.method+" "+r.path] {
			t.Errorf("unexpected flask route %s %s", r.method, r.path)
		}
	}
}

func TestDjangoPath(t *testing.T) {
	cases := map[string]string{
		"users/<int:id>/":             "/users/{id}/",
		"^legacy/(?P<slug>[\\w-]+)/$": "/legacy/{slug}/",
		"health/":                     "/health/",
	}
	for in, want := range cases {
		if got := djangoPath(in); got != want {
			t.Errorf("djangoPath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestExecuteDjangoMethodGap(t *testing.T) {
	src := "from django.urls import path\nurlpatterns = [ path('users/<int:id>/', v) ]\n"
	in := inputDir(t, "urls.py", src)
	out := t.TempDir()
	if err := Execute(Options{Inputs: []string{in}, Out: out}); err != nil {
		t.Fatal(err)
	}
	rep := readReport(t, out).Sources[0]
	if rep.Extractor != "django" {
		t.Errorf("extractor = %q, want django", rep.Extractor)
	}
	if !hasGap(rep.Gaps, gapMethodUnverified, false) {
		t.Errorf("expected method-unverified gap, got %+v", rep.Gaps)
	}
}

func TestFindRails(t *testing.T) {
	src := `Rails.application.routes.draw do
  get "/users", to: "users#index"
  post "/users"
  get "/users/:id", to: "users#show"
end`
	if got := len(dedupeRoutes(findRails(src))); got != 3 {
		t.Errorf("rails routes = %d, want 3", got)
	}
}

func TestFindLaravelOptionalParam(t *testing.T) {
	src := `Route::get("/users", x);
Route::delete("/users/{id?}", y);`
	routes := dedupeRoutes(findLaravel(src))
	want := map[string]bool{"GET /users": true, "DELETE /users/{id}": true}
	if len(routes) != 2 {
		t.Fatalf("laravel routes = %d, want 2: %+v", len(routes), routes)
	}
	for _, r := range routes {
		if !want[r.method+" "+r.path] {
			t.Errorf("unexpected laravel route %s %s", r.method, r.path)
		}
	}
}

func TestFindAspNet(t *testing.T) {
	src := `[HttpGet("users/{id}")] public X Get(){}
[HttpPost("users")] public X Create(){}`
	if got := len(dedupeRoutes(findAspNet(src))); got != 2 {
		t.Errorf("aspnet routes = %d, want 2", got)
	}
}

func TestFindKtorExcludesGetters(t *testing.T) {
	src := `routing { get("/health") { }; post("/users") { }; val v = map.get("nope") }`
	routes := dedupeRoutes(findKtor(src))
	if len(routes) != 2 {
		t.Fatalf("ktor routes = %d, want 2: %+v", len(routes), routes)
	}
	for _, r := range routes {
		if r.path == "/nope" {
			t.Error("map.get became a route")
		}
	}
}

func TestFindActixAndAxum(t *testing.T) {
	actix := `#[get("/users/{id}")]
#[post("/users")]`
	if got := len(dedupeRoutes(findActix(actix))); got != 2 {
		t.Errorf("actix routes = %d, want 2", got)
	}
	axum := `Router::new().route("/health", get(h)).route("/users", post(c))`
	routes := dedupeRoutes(findAxum(axum))
	want := map[string]bool{"GET /health": true, "POST /users": true}
	if len(routes) != 2 {
		t.Fatalf("axum routes = %d, want 2: %+v", len(routes), routes)
	}
	for _, r := range routes {
		if !want[r.method+" "+r.path] {
			t.Errorf("unexpected axum route %s %s", r.method, r.path)
		}
	}
}

func hasPrefix(s, p string) bool { return len(s) >= len(p) && s[:len(p)] == p }

func asNoSources(err error, target *ErrNoSources) bool {
	e, ok := err.(ErrNoSources)
	if ok {
		*target = e
	}
	return ok
}

// Draft must re-parse as openapi3 with one operation per recovered route.
func TestSynthesizeRoundTrip(t *testing.T) {
	routes := []route{
		{"GET", "/health"},
		{"GET", "/users/{id}"},
		{"POST", "/users"},
	}
	draft := synthesizeOpenAPI("demo", "fastapi", routes)
	p, err := parseSpec(draft)
	if err != nil {
		t.Fatalf("synthesized draft failed to parse: %v", err)
	}
	if p == nil || p.format != "openapi3" {
		t.Fatalf("draft not recognized as openapi3: %+v", p)
	}
	if p.metrics.Operations != len(routes) {
		t.Errorf("operations = %d, want %d", p.metrics.Operations, len(routes))
	}
}

func TestExecuteL2FastAPI(t *testing.T) {
	in := inputDir(t, "app/main.py", fastAPISrc)
	out := t.TempDir()
	if err := Execute(Options{Inputs: []string{in}, Out: out}); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	s := firstSource(t, filepath.Join(out, sourcesFileName))
	if s["backend"] != "openapi3" {
		t.Fatalf("backend = %v", s["backend"])
	}
	rep := readReport(t, out).Sources[0]
	if rep.Level != "L2" || rep.Extractor != "fastapi" {
		t.Errorf("level/extractor = %q/%q, want L2/fastapi", rep.Level, rep.Extractor)
	}
	if rep.Confidence != confMedium {
		t.Errorf("confidence = %q, want medium", rep.Confidence)
	}
	if rep.WouldEmitCommands != 3 {
		t.Errorf("wouldEmit = %d, want 3", rep.WouldEmitCommands)
	}
	if _, err := readSourcesFileExists(filepath.Join(out, s["local_path"].(string), l2DraftName)); err != nil {
		t.Errorf("synthesized draft missing: %v", err)
	}
}

// L2 must not run when a usable native L1 spec exists.
func TestL2SuppressedByL1(t *testing.T) {
	in := t.TempDir()
	writeFile(t, in, "openapi.yaml", specOpenAPI)
	writeFile(t, in, "app.py", fastAPISrc)

	out := t.TempDir()
	if err := Execute(Options{Inputs: []string{in}, Out: out}); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	for _, s := range readReport(t, out).Sources {
		if s.Level == "L2" {
			t.Errorf("L2 ran despite a usable L1 spec: %+v", s)
		}
	}
}

func readSourcesFileExists(path string) (bool, error) {
	if _, err := readCapped(path); err != nil {
		return false, err
	}
	return true, nil
}
