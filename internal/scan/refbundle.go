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

	b.walk(doc, filepath.Dir(primaryAbs))

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

func (b *bundler) walk(node any, baseDir string) {
	switch t := node.(type) {
	case map[string]any:
		if ref, ok := t["$ref"].(string); ok && ref != "" {
			if strings.HasPrefix(ref, "#") {
				return
			}
			if name, ok := b.resolveExternal(ref, baseDir); ok {
				t["$ref"] = "#/" + strings.Join(b.section, "/") + "/" + name
			} else {
				b.missing = append(b.missing, ref)
			}
			return
		}
		for _, v := range t {
			b.walk(v, baseDir)
		}
	case []any:
		for _, v := range t {
			b.walk(v, baseDir)
		}
	}
}

func (b *bundler) resolveExternal(ref, baseDir string) (string, bool) {
	filePart, frag, _ := strings.Cut(ref, "#")
	if filePart == "" {
		// In-document ref with a nonstandard prefix; leave as missing.
		return "", false
	}
	absFile := filepath.Clean(filepath.Join(baseDir, filepath.FromSlash(filePart)))
	if !pathUnderRoot(b.root, absFile) {
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
	b.walk(target, filepath.Dir(absFile))
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
