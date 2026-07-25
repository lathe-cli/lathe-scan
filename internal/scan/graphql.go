package scan

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/ast"
)

// Lathe requires an explicit expose policy (refuses whole-schema expose).
func buildGraphQLSource(files []string, root string, git *gitOrigin) (*builtSource, *Candidate) {
	var sources []*ast.Source
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil || len(data) > maxSpecBytes {
			continue
		}
		sources = append(sources, &ast.Source{Name: relOrBase(root, f), Input: string(data)})
	}
	if len(sources) == 0 {
		return nil, nil
	}

	primary := pickPrimaryGraphQL(files, root)
	cand := &Candidate{Path: primary, Format: "graphql"}

	schema, err := gqlparser.LoadSchema(sources...)
	if err != nil {
		cand.Error = err.Error()
		return nil, cand
	}
	queries := fieldNames(schema.Query)
	mutations := fieldNames(schema.Mutation)
	ops := len(queries) + len(mutations)
	cand.Parsed = true
	cand.Metrics = &Metrics{Operations: ops}
	cand.Reason = fmt.Sprintf("graphql, %d queries, %d mutations", len(queries), len(mutations))

	repoName := ""
	if git != nil {
		repoName = git.repoName
	}
	b := &builtSource{
		baseName: firstNonEmpty(sanitizeName(repoName), sanitizeName(parentDirName(primary)), "graphql"),
		identity: "graphql",
		yc:       &ycSource{Backend: "graphql"},
	}
	block := &graphqlBlock{
		Schema: primary,
		Expose: graphqlExpose{Queries: queries, Mutations: mutations},
	}
	if git != nil {
		b.origin = &Origin{Type: "repo_url", RepoURL: git.repoURL, PinnedTag: git.pinnedTag, RefKind: git.refKind}
		b.yc.RepoURL = git.repoURL
		b.yc.PinnedTag = git.pinnedTag
	} else {
		b.origin = &Origin{Type: "local_path"}
		base := filepath.Base(primary)
		block.Schema = base
		b.copies = []copyItem{{absFrom: filepath.Join(root, filepath.FromSlash(primary)), relTo: base}}
	}
	b.yc.GraphQL = block

	var gaps []Gap
	if ops == 0 {
		gaps = append(gaps, Gap{Kind: gapParseError, Scope: "source",
			Message: "schema declares no queries or mutations; graphql.expose would be empty and Lathe rejects it", Blocking: true})
	} else {
		gaps = append(gaps, Gap{Kind: gapGraphQLExpose, Scope: "source",
			Message: "expose lists every discovered query and mutation; trim to the intended surface before generating", Blocking: false})
	}
	if len(sources) > 1 {
		gaps = append(gaps, Gap{Kind: gapGraphQLSplit, Scope: "source",
			Message: fmt.Sprintf("schema is split across %d files; Lathe loads only graphql.schema=%s, so merge them first", len(sources), block.Schema), Blocking: true})
	}

	b.report = &SourceReport{
		Name: "", Level: "L1", Backend: "graphql",
		WouldEmitCommands: ops,
		Files:             []string{block.Schema},
		Metrics:           &Metrics{Operations: ops},
		Gaps:              gaps,
		Confidence:        confidenceFor(ops, gaps),
	}
	return b, cand
}

func fieldNames(def *ast.Definition) []string {
	if def == nil {
		return nil
	}
	var out []string
	for _, f := range def.Fields {
		if strings.HasPrefix(f.Name, "__") {
			continue
		}
		out = append(out, f.Name)
	}
	sort.Strings(out)
	return out
}

func pickPrimaryGraphQL(files []string, root string) string {
	var withQuery, first string
	for _, f := range files {
		rel := relOrBase(root, f)
		if first == "" {
			first = rel
		}
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		s := string(data)
		if strings.Contains(s, "type Query") || strings.Contains(s, "schema ") || strings.Contains(s, "schema{") {
			if withQuery == "" {
				withQuery = rel
			}
		}
	}
	if withQuery != "" {
		return withQuery
	}
	return first
}
