package scan

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	protoDependencyBuf      = "buf"
	protoDependencyGoModule = "go_module"
)

// Provider trees a repository vendors for itself. Discovery skips these on
// purpose — a spec found there belongs to a dependency, not to the repo — but a
// proto living there is still the only local answer to an import like
// google/api/annotations.proto, and refusing to look would drop a source that
// compiles perfectly well.
var protoVendorDirs = []string{"third_party", "third-party", "vendor"}

// third_party/<project>/<import path> is the common shape; one level is enough
// to find it without turning resolution into a full second walk.
const protoVendorSubdirCap = 64

type protoResolution struct {
	closure      []string
	vendored     []vendoredProto
	vendorRoots  []string
	dependencies []protoDependency
	missing      []protoMissingImport
	modulePath   string
	moduleRoot   string
	moduleMapped bool
}

// A provider proto pulled from a skipped tree. rel is the import path itself:
// that is the only name under which protoc will look for it.
type vendoredProto struct {
	abs string
	rel string
}

type protoMissingImport struct {
	importer string
	path     string
}

type goModuleMetadata struct {
	path     string
	root     string
	requires map[string]string
	sums     map[string]string
}

type bufLockFile struct {
	Version string `yaml:"version"`
	Deps    []struct {
		Name       string `yaml:"name"`
		Remote     string `yaml:"remote"`
		Owner      string `yaml:"owner"`
		Repository string `yaml:"repository"`
		Commit     string `yaml:"commit"`
		Digest     string `yaml:"digest"`
	} `yaml:"deps"`
}

func resolveProtoClosure(files []string, infos []protoFileInfo, root string) protoResolution {
	byAbs := make(map[string]protoFileInfo, len(files))
	byRel := make(map[string]string, len(files))
	for i, file := range files {
		clean := filepath.Clean(file)
		byAbs[clean] = infos[i]
		byRel[filepath.ToSlash(infos[i].rel)] = clean
	}

	entries := make([]string, 0, len(files))
	for _, file := range files {
		if byAbs[filepath.Clean(file)].services > 0 {
			entries = append(entries, filepath.Clean(file))
		}
	}
	if len(entries) == 0 {
		for _, file := range files {
			entries = append(entries, filepath.Clean(file))
		}
	}

	module := readGoModuleMetadata(root, entries)
	bufDeps := readBufDependencies(root, entries)
	resolved := map[string]bool{}
	vendoredSeen := map[string]bool{}
	vendorRoots := map[string]bool{}
	dependencies := map[string]protoDependency{}
	var vendored []vendoredProto
	var missing []protoMissingImport
	moduleMapped := false

	var visit func(string)
	var visitVendored func(string, string, []byte)
	resolveImports := func(importer, fromDir string, imports []string) {
		for _, imp := range imports {
			// Only the well-known types ship with protoc itself; everything else
			// has to come from the repository or a pinned dependency.
			if strings.HasPrefix(imp, "google/protobuf/") {
				continue
			}
			if module.path != "" && strings.HasPrefix(imp, module.path+"/") {
				target := filepath.Clean(filepath.Join(module.root, filepath.FromSlash(strings.TrimPrefix(imp, module.path+"/"))))
				if _, ok := byAbs[target]; ok {
					moduleMapped = true
					visit(target)
					continue
				}
			}
			if target, ok := resolveLocalProtoImport(imp, byRel); ok {
				visit(target)
				continue
			}
			if abs, vendorRoot, data, ok := probeVendoredProto(root, fromDir, imp, byAbs); ok {
				vendorRoots[vendorRoot] = true
				visitVendored(abs, imp, data)
				continue
			}
			if dep, ok := resolveExternalProtoImport(imp, module, bufDeps); ok {
				dependencies[protoDependencyKey(dep)] = dep
				continue
			}
			missing = append(missing, protoMissingImport{importer: importer, path: imp})
		}
	}

	visit = func(file string) {
		if resolved[file] {
			return
		}
		resolved[file] = true
		info := byAbs[file]
		resolveImports(info.rel, filepath.Dir(file), info.imports)
	}
	visitVendored = func(abs, importPath string, data []byte) {
		if vendoredSeen[abs] {
			return
		}
		vendoredSeen[abs] = true
		vendored = append(vendored, vendoredProto{abs: abs, rel: importPath})
		resolveImports(importPath, filepath.Dir(abs), analyzeProto(importPath, data).imports)
	}
	for _, entry := range entries {
		visit(entry)
	}

	closure := make([]string, 0, len(resolved))
	for file := range resolved {
		closure = append(closure, file)
	}
	sort.Strings(closure)
	// The module prefix rewrites every emitted path against one module root, so
	// it is only honest when the whole closure actually lives under that root. A
	// second module in the same tree would otherwise be published under the
	// first module's path — a name that resolves to nothing.
	// The module path is likewise scanned source, and it prefixes every emitted
	// path, so a non-local one would send entries and copies out of the tree.
	if moduleMapped && !filepath.IsLocal(filepath.FromSlash(module.path)) {
		moduleMapped = false
	}
	if moduleMapped {
		for _, file := range closure {
			if !isWithin(module.root, file) {
				moduleMapped = false
				break
			}
		}
	}
	sort.Slice(vendored, func(i, j int) bool { return vendored[i].rel < vendored[j].rel })
	sort.Slice(missing, func(i, j int) bool {
		if missing[i].importer != missing[j].importer {
			return missing[i].importer < missing[j].importer
		}
		return missing[i].path < missing[j].path
	})
	roots := make([]string, 0, len(vendorRoots))
	for vendorRoot := range vendorRoots {
		roots = append(roots, vendorRoot)
	}
	sort.Strings(roots)
	deps := make([]protoDependency, 0, len(dependencies))
	for _, dep := range dependencies {
		deps = append(deps, dep)
	}
	sort.Slice(deps, func(i, j int) bool { return protoDependencyKey(deps[i]) < protoDependencyKey(deps[j]) })
	return protoResolution{
		closure:      closure,
		vendored:     vendored,
		vendorRoots:  roots,
		dependencies: deps,
		missing:      missing,
		modulePath:   module.path,
		moduleRoot:   module.root,
		moduleMapped: moduleMapped,
	}
}

func resolveLocalProtoImport(imp string, byRel map[string]string) (string, bool) {
	if file, ok := byRel[imp]; ok {
		return file, true
	}
	suffix := "/" + imp
	var match string
	for rel, file := range byRel {
		if strings.HasSuffix(rel, suffix) {
			if match != "" {
				return "", false
			}
			match = file
		}
	}
	return match, match != ""
}

// probeVendoredProto searches the input tree for an import that discovery
// skipped, bounded to ancestors of the importer up to the input root, the
// vendor directories under them, and one level inside those. Files that were
// indexed are left to resolveLocalProtoImport: reaching one here would mean it
// was ambiguous by name, and picking the nearest copy would silently choose a
// meaning protoc might not agree with.
func probeVendoredProto(root, fromDir, imp string, indexed map[string]protoFileInfo) (string, string, []byte, bool) {
	suffix := filepath.FromSlash(imp)
	// The import string doubles as the name the file is staged under, and it
	// comes from scanned source. One that escapes its own root cannot name a
	// staged file, and copying to it would write outside --out.
	if !filepath.IsLocal(suffix) {
		return "", "", nil, false
	}
	hit := func(dir string) (string, []byte, bool) {
		path := filepath.Join(dir, suffix)
		if _, ok := indexed[filepath.Clean(path)]; ok {
			return "", nil, false
		}
		data, err := readWithin(root, path)
		if err != nil {
			return "", nil, false
		}
		return path, data, true
	}
	for dir := filepath.Clean(fromDir); ; {
		if path, data, ok := hit(dir); ok {
			return path, dir, data, true
		}
		for _, vendor := range protoVendorDirs {
			base := filepath.Join(dir, vendor)
			if path, data, ok := hit(base); ok {
				return path, base, data, true
			}
			for _, sub := range vendorSubdirs(base) {
				if path, data, ok := hit(sub); ok {
					return path, sub, data, true
				}
			}
		}
		if samePath(dir, root) {
			return "", "", nil, false
		}
		parent := filepath.Dir(dir)
		if parent == dir || !isWithin(root, parent) {
			return "", "", nil, false
		}
		dir = parent
	}
}

func vendorSubdirs(dir string) []string {
	names, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	subs := make([]string, 0, len(names))
	for _, name := range names {
		if !name.IsDir() {
			continue
		}
		if len(subs) == protoVendorSubdirCap {
			break
		}
		subs = append(subs, filepath.Join(dir, name.Name()))
	}
	return subs
}

func resolveExternalProtoImport(imp string, module goModuleMetadata, bufDeps []protoDependency) (protoDependency, bool) {
	if isGoogleAPIImport(imp) {
		for _, dep := range bufDeps {
			if dep.Module == "buf.build/googleapis/googleapis" {
				return dep, true
			}
		}
		if version, sum, ok := module.pin("github.com/grpc-ecosystem/grpc-gateway"); ok {
			return protoDependency{
				Kind:    protoDependencyGoModule,
				Module:  "github.com/grpc-ecosystem/grpc-gateway",
				Version: version,
				Sum:     sum,
				Staging: []stagingEntry{{From: "third_party/googleapis", To: "."}},
			}, true
		}
	}
	if strings.HasPrefix(imp, "gogoproto/") {
		if version, sum, ok := module.pin("github.com/gogo/protobuf"); ok {
			return protoDependency{
				Kind:    protoDependencyGoModule,
				Module:  "github.com/gogo/protobuf",
				Version: version,
				Sum:     sum,
				Staging: []stagingEntry{{From: ".", To: "."}},
			}, true
		}
	}

	var best string
	for required := range module.requires {
		if (imp == required || strings.HasPrefix(imp, required+"/")) && len(required) > len(best) {
			best = required
		}
	}
	version, sum, ok := module.pin(best)
	if best == "" || !ok {
		return protoDependency{}, false
	}
	return protoDependency{
		Kind:    protoDependencyGoModule,
		Module:  best,
		Version: version,
		Sum:     sum,
		Staging: []stagingEntry{{From: ".", To: best}},
	}, true
}

func isGoogleAPIImport(path string) bool {
	return strings.HasPrefix(path, "google/api/") ||
		strings.HasPrefix(path, "google/type/") ||
		strings.HasPrefix(path, "google/longrunning/")
}

// pin returns a module version only when go.sum carries a module hash for it.
// Lathe verifies that hash against `go mod download`, so a pin it cannot check
// is not a pin — emitting one would trade a blocking gap for a manifest that
// fails at sync time.
func (m goModuleMetadata) pin(module string) (string, string, bool) {
	version := m.requires[module]
	if version == "" {
		return "", "", false
	}
	sum := m.sums[module+"@"+version]
	if len(sum) <= len("h1:") || !strings.HasPrefix(sum, "h1:") {
		return "", "", false
	}
	return version, sum, true
}

func readGoModuleMetadata(root string, entries []string) goModuleMetadata {
	if len(entries) == 0 {
		return goModuleMetadata{}
	}
	dir := filepath.Dir(entries[0])
	for {
		path := filepath.Join(dir, "go.mod")
		if data, err := readWithin(root, path); err == nil {
			meta := parseGoModule(string(data))
			meta.root = dir
			meta.sums = readGoSums(root, filepath.Join(dir, "go.sum"))
			return meta
		}
		if samePath(dir, root) {
			return goModuleMetadata{}
		}
		parent := filepath.Dir(dir)
		if parent == dir || !isWithin(root, parent) {
			return goModuleMetadata{}
		}
		dir = parent
	}
}

func parseGoModule(data string) goModuleMetadata {
	meta := goModuleMetadata{requires: map[string]string{}, sums: map[string]string{}}
	scanner := bufio.NewScanner(strings.NewReader(data))
	inRequire := false
	for scanner.Scan() {
		line := strings.TrimSpace(strings.SplitN(scanner.Text(), "//", 2)[0])
		switch {
		case line == "require (":
			inRequire = true
			continue
		case inRequire && line == ")":
			inRequire = false
			continue
		}
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] == "module" {
			meta.path = fields[1]
		} else if len(fields) >= 3 && fields[0] == "require" {
			meta.requires[fields[1]] = fields[2]
		} else if inRequire && len(fields) >= 2 {
			meta.requires[fields[0]] = fields[1]
		}
	}
	return meta
}

func readGoSums(root, path string) map[string]string {
	sums := map[string]string{}
	data, err := readWithin(root, path)
	if err != nil {
		return sums
	}
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) == 3 && !strings.HasSuffix(fields[1], "/go.mod") {
			sums[fields[0]+"@"+fields[1]] = fields[2]
		}
	}
	return sums
}

func readBufDependencies(root string, entries []string) []protoDependency {
	seen := map[string]bool{}
	var deps []protoDependency
	for _, entry := range entries {
		dir := filepath.Dir(entry)
		for {
			lockPath := filepath.Join(dir, "buf.lock")
			if !seen[lockPath] {
				seen[lockPath] = true
				if data, err := readWithin(root, lockPath); err == nil {
					var lock bufLockFile
					if yaml.Unmarshal(data, &lock) == nil {
						for _, pin := range lock.Deps {
							name := pin.Name
							if name == "" && pin.Remote != "" && pin.Owner != "" && pin.Repository != "" {
								name = strings.TrimSuffix(pin.Remote, "/") + "/" + pin.Owner + "/" + pin.Repository
							}
							if name == "" || !validBufPin(lock.Version, pin.Commit, pin.Digest) {
								continue
							}
							deps = append(deps, protoDependency{
								Kind:        protoDependencyBuf,
								Module:      name,
								Commit:      pin.Commit,
								Digest:      pin.Digest,
								LockVersion: lock.Version,
								Staging:     []stagingEntry{{From: ".", To: "."}},
							})
						}
					}
				}
			}
			if samePath(dir, root) {
				break
			}
			parent := filepath.Dir(dir)
			if parent == dir || !isWithin(root, parent) {
				break
			}
			dir = parent
		}
	}
	return deps
}

// validBufPin mirrors what Lathe accepts in proto.dependencies. buf has shipped
// several digest formats; writing one Lathe refuses would fail the whole
// manifest at load time, which is a worse answer than an unresolved import.
func validBufPin(lockVersion, commit, digest string) bool {
	prefix := "b5:"
	switch lockVersion {
	case "v1":
		prefix = "b4:"
	case "v2":
	default:
		return false
	}
	if len(commit) != 32 || !isLowerHex(commit) {
		return false
	}
	rest, ok := strings.CutPrefix(digest, prefix)
	return ok && rest != "" && isLowerHex(rest)
}

func isLowerHex(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

func protoDependencyKey(dep protoDependency) string {
	return fmt.Sprintf("%s\x00%s\x00%s\x00%s\x00%s", dep.Kind, dep.Module, dep.Version, dep.Commit, dep.LockVersion)
}

func samePath(a, b string) bool {
	return filepath.Clean(a) == filepath.Clean(b)
}

func isWithin(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
