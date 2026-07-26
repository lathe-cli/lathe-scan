package scan

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Lathe's openapi3/swagger backends keep external file $refs raw (only
// #/components/schemas refs are rewritten). Multi-file specs must be bundled:
// external refs inlined into the root schema namespace and rewritten internal.

type bundler struct {
	root      string // scan root; external refs must stay under it
	primary   string // abs path of the root document; its "#/" refs are already internal
	section   []string
	hoisted   map[string]any
	assigned  map[string]string
	usedNames map[string]bool
	fileCache map[string]map[string]any
	files     map[string]bool
	missing   []string
}

func bundleSpec(primaryAbs, format, scanRoot string) (out []byte, files []string, missing []string, err error) {
	b := &bundler{
		root:      scanRoot,
		hoisted:   map[string]any{},
		assigned:  map[string]string{},
		usedNames: map[string]bool{},
		fileCache: map[string]map[string]any{},
		files:     map[string]bool{},
	}
	if format == "swagger" {
		b.section = []string{"definitions"}
	} else {
		b.section = []string{"components", "schemas"}
	}

	b.primary = primaryAbs

	doc, err := b.load(primaryAbs)
	if err != nil {
		return nil, nil, nil, err
	}
	b.files[primaryAbs] = true

	// Reserve existing names so hoisted schemas don't collide.
	if sec := getSection(doc, b.section); sec != nil {
		for k := range sec {
			b.usedNames[k] = true
		}
	}

	b.walk(doc, primaryAbs)

	if len(b.hoisted) > 0 {
		sec := ensureSection(doc, b.section)
		for name, node := range b.hoisted {
			sec[name] = node
		}
	}

	data, err := yaml.Marshal(doc)
	if err != nil {
		return nil, nil, nil, err
	}
	return data, sortedKeysBool(b.files), b.missing, nil
}

// walk rewrites every $ref reachable from a node that came from srcFile.
//
// srcFile (not just its directory) is what makes hoisting correct: a "#/..."
// ref inside a hoisted fragment is document-relative to the file it came from,
// so once that fragment moves into the root document the ref has to be
// re-resolved against its original file. Treating it as already-internal
// silently re-points it at whatever the root document happens to call the same
// name — the caller's schema then means something else entirely.
//
// Map keys are visited in sorted order: hoisted names are assigned first-come,
// so an unordered walk would hand the unsuffixed name to a different schema
// from run to run and break byte-identical output.
func (b *bundler) walk(node any, srcFile string) {
	switch t := node.(type) {
	case map[string]any:
		if ref, ok := t["$ref"].(string); ok && ref != "" {
			if name, ok := b.resolveRef(ref, srcFile); ok {
				t["$ref"] = "#/" + strings.Join(b.section, "/") + "/" + name
			} else if !b.isRootInternal(ref, srcFile) {
				b.missing = append(b.missing, ref)
			}
			return
		}
		for _, k := range sortedKeysAny(t) {
			b.walk(t[k], srcFile)
		}
	case []any:
		for _, v := range t {
			b.walk(v, srcFile)
		}
	}
}

// isRootInternal reports whether a ref needs no rewriting at all: an in-document
// ref that already lives in the primary document.
func (b *bundler) isRootInternal(ref, srcFile string) bool {
	return strings.HasPrefix(ref, "#") && srcFile == b.primary
}

// resolveRef hoists both external refs and the in-document refs of an already
// hoisted fragment. Returns the assigned schema name in the root namespace.
func (b *bundler) resolveRef(ref, srcFile string) (string, bool) {
	if strings.HasPrefix(ref, "#") {
		if srcFile == b.primary {
			return "", false // already in the root namespace
		}
		return b.resolveExternal(filepath.Base(srcFile)+ref, filepath.Dir(srcFile))
	}
	return b.resolveExternal(ref, filepath.Dir(srcFile))
}

func (b *bundler) resolveExternal(ref, baseDir string) (string, bool) {
	filePart, frag, _ := strings.Cut(ref, "#")
	if filePart == "" {
		// In-document ref with a nonstandard prefix; leave as missing.
		return "", false
	}
	// Only schema-section refs can be hoisted into components/schemas
	// (definitions). External refs to path items, parameters, responses, or
	// whole files are not schemas — bundling them there would silently produce an
	// invalid spec, so leave them unresolved and let the caller flag a gap.
	if !b.isSchemaFragment(frag) {
		return "", false
	}
	// Physical resolution, not lexical: a symlink inside the input can point at
	// an arbitrary file on the machine, and Clean/Join would still call it "under
	// the root". An unresolvable or escaping target is left missing, which the
	// caller turns into a blocking gap.
	absFile, ok := resolveWithin(b.root, filepath.Join(baseDir, filepath.FromSlash(filePart)))
	if !ok {
		return "", false
	}
	key := absFile + "#" + frag
	if n, ok := b.assigned[key]; ok {
		return n, true
	}

	doc, err := b.load(absFile)
	if err != nil {
		return "", false
	}
	b.files[absFile] = true

	target, ok := extractFragment(doc, frag)
	if !ok {
		return "", false
	}
	name := b.uniqueName(fragName(frag, filePart))
	b.assigned[key] = name // before recursing so cycles terminate
	b.walk(target, absFile)
	b.hoisted[name] = target
	return name, true
}

func (b *bundler) load(abs string) (map[string]any, error) {
	if doc, ok := b.fileCache[abs]; ok {
		return doc, nil
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return nil, fmt.Errorf("read ref %s: %w", abs, err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parse ref %s: %w", abs, err)
	}
	b.fileCache[abs] = doc
	return doc, nil
}

func (b *bundler) uniqueName(base string) string {
	base = sanitizeSchemaName(base)
	if base == "" {
		base = "Ref"
	}
	name := base
	for i := 2; b.usedNames[name]; i++ {
		name = fmt.Sprintf("%s_%d", base, i)
	}
	b.usedNames[name] = true
	return name
}

// Empty fragment is a whole-file ref.
func extractFragment(doc map[string]any, frag string) (any, bool) {
	if frag == "" || frag == "/" {
		return doc, true
	}
	var cur any = doc
	for _, seg := range strings.Split(strings.TrimPrefix(frag, "/"), "/") {
		seg = strings.ReplaceAll(strings.ReplaceAll(seg, "~1", "/"), "~0", "~")
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		cur, ok = m[seg]
		if !ok {
			return nil, false
		}
	}
	return cur, true
}

func fragName(frag, filePart string) string {
	if frag != "" && frag != "/" {
		parts := strings.Split(strings.Trim(frag, "/"), "/")
		return parts[len(parts)-1]
	}
	base := filepath.Base(filePart)
	return strings.TrimSuffix(base, filepath.Ext(base))
}

func getSection(root map[string]any, section []string) map[string]any {
	cur := root
	for _, k := range section {
		next, ok := cur[k].(map[string]any)
		if !ok {
			return nil
		}
		cur = next
	}
	return cur
}

func ensureSection(root map[string]any, section []string) map[string]any {
	cur := root
	for _, k := range section {
		next, ok := cur[k].(map[string]any)
		if !ok {
			next = map[string]any{}
			cur[k] = next
		}
		cur = next
	}
	return cur
}

func sanitizeSchemaName(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func sortedKeysBool(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedKeysAny(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// isSchemaFragment reports whether a ref fragment points into the schema section
// (/components/schemas/... or /definitions/...). Whole-file refs (empty
// fragment) are treated as non-schema, since their kind is unknown.
func (b *bundler) isSchemaFragment(frag string) bool {
	prefix := "/" + strings.Join(b.section, "/") + "/"
	return strings.HasPrefix(frag, prefix) && len(frag) > len(prefix)
}

// pathUnderRoot rejects $ref targets that escape the scanned tree.
func pathUnderRoot(root, path string) bool {
	if root == "" {
		return false
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
