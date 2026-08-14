package architecture

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
)

type goSource struct {
	path string
	tree *ast.File
}

func TestIdentifierNamesDoNotShadowDeclarations(t *testing.T) {
	t.Parallel()
	fileSet := token.NewFileSet()
	sourcesByPackage := parsePackages(t, fileSet, repositoryRoot(t))
	findings := []string{}
	for _, sources := range sourcesByPackage {
		declaredNames := collectDeclaredNames(sources)
		for _, source := range sources {
			findings = append(findings, inspectBindings(fileSet, source, declaredNames)...)
		}
	}
	if len(findings) == 0 {
		return
	}
	sort.Strings(findings)
	t.Fatalf("variables must not reuse function or type names, including capitalization-only variants:\n%s", strings.Join(findings, "\n"))
}

func TestNamingAuditFindsCollisions(t *testing.T) {
	t.Parallel()
	fileSet := token.NewFileSet()
	tree, err := parser.ParseFile(fileSet, "fixture.go", `package fixture
type Model struct{}
func Client() {}
func Build(model Model) {
	client := 1
	_ = client
}
`, 0)
	if err != nil {
		t.Fatal(err)
	}
	source := goSource{path: "fixture.go", tree: tree}
	findings := inspectBindings(fileSet, source, collectDeclaredNames([]goSource{source}))
	if len(findings) != 2 {
		t.Fatalf("findings = %#v, want model/Model and client/Client collisions", findings)
	}
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve naming audit source path")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(currentFile), "..", ".."))
}

func parsePackages(t *testing.T, fileSet *token.FileSet, root string) map[string][]goSource {
	t.Helper()
	sourcesByPackage := map[string][]goSource{}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() && (entry.Name() == ".git" || entry.Name() == "vendor") {
			return filepath.SkipDir
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		tree, parseErr := parser.ParseFile(fileSet, path, nil, parser.ParseComments)
		if parseErr != nil {
			return parseErr
		}
		if generatedSource(tree) {
			return nil
		}
		packageKey := filepath.Dir(path) + ":" + tree.Name.Name
		sourcesByPackage[packageKey] = append(sourcesByPackage[packageKey], goSource{path: path, tree: tree})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return sourcesByPackage
}

func generatedSource(tree *ast.File) bool {
	for _, group := range tree.Comments {
		text := group.Text()
		if strings.HasPrefix(text, "Code generated ") && strings.Contains(text, "DO NOT EDIT.") {
			return true
		}
	}
	return false
}

func collectDeclaredNames(sources []goSource) map[string]string {
	declaredNames := map[string]string{}
	for _, source := range sources {
		ast.Inspect(source.tree, func(node ast.Node) bool {
			switch declaration := node.(type) {
			case *ast.FuncDecl:
				declaredNames[strings.ToLower(declaration.Name.Name)] = declaration.Name.Name
			case *ast.TypeSpec:
				declaredNames[strings.ToLower(declaration.Name.Name)] = declaration.Name.Name
			}
			return true
		})
	}
	return declaredNames
}

func inspectBindings(fileSet *token.FileSet, source goSource, declaredNames map[string]string) []string {
	findings := []string{}
	ast.Inspect(source.tree, func(node ast.Node) bool {
		switch statement := node.(type) {
		case *ast.FuncDecl:
			checkFieldList(fileSet, statement.Recv, declaredNames, &findings)
		case *ast.FuncType:
			checkFieldList(fileSet, statement.Params, declaredNames, &findings)
			checkFieldList(fileSet, statement.Results, declaredNames, &findings)
		case *ast.AssignStmt:
			if statement.Tok == token.DEFINE {
				checkExpressions(fileSet, statement.Lhs, declaredNames, &findings)
			}
		case *ast.RangeStmt:
			if statement.Tok == token.DEFINE {
				checkExpressions(fileSet, []ast.Expr{statement.Key, statement.Value}, declaredNames, &findings)
			}
		case *ast.GenDecl:
			if statement.Tok == token.VAR {
				for _, specification := range statement.Specs {
					valueSpec := specification.(*ast.ValueSpec)
					for _, identifier := range valueSpec.Names {
						checkIdentifier(fileSet, identifier, declaredNames, &findings)
					}
				}
			}
		}
		return true
	})
	return findings
}

func checkFieldList(fileSet *token.FileSet, fields *ast.FieldList, declaredNames map[string]string, findings *[]string) {
	if fields == nil {
		return
	}
	for _, field := range fields.List {
		for _, identifier := range field.Names {
			checkIdentifier(fileSet, identifier, declaredNames, findings)
		}
	}
}

func checkExpressions(fileSet *token.FileSet, expressions []ast.Expr, declaredNames map[string]string, findings *[]string) {
	for _, expression := range expressions {
		identifier, ok := expression.(*ast.Ident)
		if ok {
			checkIdentifier(fileSet, identifier, declaredNames, findings)
		}
	}
}

func checkIdentifier(fileSet *token.FileSet, identifier *ast.Ident, declaredNames map[string]string, findings *[]string) {
	if identifier == nil || identifier.Name == "_" {
		return
	}
	declarationName, exists := declaredNames[strings.ToLower(identifier.Name)]
	if !exists {
		return
	}
	position := fileSet.Position(identifier.Pos())
	*findings = append(*findings, fmt.Sprintf(
		"%s:%d: %s collides with %s", position.Filename, position.Line, identifier.Name, declarationName,
	))
}
