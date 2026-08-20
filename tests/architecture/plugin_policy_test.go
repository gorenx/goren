package architecture

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

func TestPluginRuntimeUsesSemanticGenericConstraints(t *testing.T) {
	t.Parallel()
	fileSet, sources := parsePluginProduction(t)
	findings := make([]string, 0)
	for _, source := range sources {
		ast.Inspect(source.tree, func(node ast.Node) bool {
			var parameters *ast.FieldList
			switch declaration := node.(type) {
			case *ast.TypeSpec:
				parameters = declaration.TypeParams
			case *ast.FuncType:
				parameters = declaration.TypeParams
			}
			if parameters == nil {
				return true
			}
			for _, field := range parameters.List {
				if unconstrainedType(field.Type) {
					position := fileSet.Position(field.Type.Pos())
					findings = append(findings, position.String())
				}
			}
			return true
		})
	}
	if len(findings) == 0 {
		return
	}
	sort.Strings(findings)
	t.Fatalf("plugin generic parameters must use semantic constraints, not any or interface{}:\n%s", strings.Join(findings, "\n"))
}

func TestPluginLegacyRuntimeSymbolsAreAbsent(t *testing.T) {
	t.Parallel()
	fileSet, sources := parsePluginProduction(t)
	forbidden := map[string]struct{}{
		"Bail":             {},
		"DecisionHandler":  {},
		"Disposer":         {},
		"DisposeFunc":      {},
		"Effect":           {},
		"EffectFunc":       {},
		"EventKey":         {},
		"EventMode":        {},
		"ModeBail":         {},
		"ModeSerial":       {},
		"ModeWaterfall":    {},
		"Next":             {},
		"NotifyHandler":    {},
		"OnDecision":       {},
		"OnNotify":         {},
		"OnWaterfall":      {},
		"Provide":          {},
		"RegisterFactory":  {},
		"Require":          {},
		"Resolve":          {},
		"ServiceKey":       {},
		"ServiceRef":       {},
		"WaterfallHandler": {},
	}
	findings := make([]string, 0)
	for _, source := range sources {
		for _, declaration := range source.tree.Decls {
			switch typedDeclaration := declaration.(type) {
			case *ast.FuncDecl:
				if typedDeclaration.Recv == nil {
					if _, exists := forbidden[typedDeclaration.Name.Name]; exists {
						findings = append(findings, fileSet.Position(typedDeclaration.Name.Pos()).String())
					}
				}
			case *ast.GenDecl:
				for _, specification := range typedDeclaration.Specs {
					typeSpecification, ok := specification.(*ast.TypeSpec)
					if !ok {
						continue
					}
					if _, exists := forbidden[typeSpecification.Name.Name]; exists {
						findings = append(findings, fileSet.Position(typeSpecification.Name.Pos()).String())
					}
				}
			}
		}
	}
	if len(findings) == 0 {
		return
	}
	sort.Strings(findings)
	t.Fatalf("legacy Plugin Runtime symbols remain:\n%s", strings.Join(findings, "\n"))
}

func TestPluginContextDoesNotExposeLegacyLifecycleMethods(t *testing.T) {
	t.Parallel()
	fileSet, sources := parsePluginProduction(t)
	forbidden := map[string]struct{}{
		"Effect":    {},
		"LoadChild": {},
	}
	findings := make([]string, 0)
	for _, source := range sources {
		for _, declaration := range source.tree.Decls {
			method, ok := declaration.(*ast.FuncDecl)
			if !ok || method.Recv == nil {
				continue
			}
			if _, exists := forbidden[method.Name.Name]; !exists {
				continue
			}
			findings = append(findings, fileSet.Position(method.Name.Pos()).String())
		}
	}
	if len(findings) == 0 {
		return
	}
	sort.Strings(findings)
	t.Fatalf("Plugin Context exposes legacy lifecycle methods:\n%s", strings.Join(findings, "\n"))
}

func TestPluginRuntimeDoesNotImportBusinessOwners(t *testing.T) {
	t.Parallel()
	_, sources := parsePluginProduction(t)
	findings := make([]string, 0)
	for _, source := range sources {
		for _, imported := range source.tree.Imports {
			importPath, err := strconv.Unquote(imported.Path.Value)
			if err != nil {
				t.Fatal(err)
			}
			if strings.HasPrefix(importPath, "github.com/gorenx/goren/") &&
				!strings.HasPrefix(importPath, "github.com/gorenx/goren/plugin") {
				findings = append(findings, source.path+":"+importPath)
			}
		}
	}
	if len(findings) == 0 {
		return
	}
	sort.Strings(findings)
	t.Fatalf("plugin must not import business owners:\n%s", strings.Join(findings, "\n"))
}

func TestPluginExportedStructsDoNotStoreFunctions(t *testing.T) {
	t.Parallel()
	fileSet, sources := parsePluginProduction(t)
	findings := make([]string, 0)
	for _, source := range sources {
		ast.Inspect(source.tree, func(node ast.Node) bool {
			typeSpecification, ok := node.(*ast.TypeSpec)
			if !ok || !typeSpecification.Name.IsExported() {
				return true
			}
			structure, ok := typeSpecification.Type.(*ast.StructType)
			if !ok {
				return true
			}
			for _, field := range structure.Fields.List {
				if _, isFunction := field.Type.(*ast.FuncType); isFunction {
					findings = append(findings, fileSet.Position(field.Pos()).String())
				}
			}
			return true
		})
	}
	if len(findings) == 0 {
		return
	}
	sort.Strings(findings)
	t.Fatalf("exported plugin structs must not store function fields:\n%s", strings.Join(findings, "\n"))
}

func unconstrainedType(expression ast.Expr) bool {
	switch constraint := expression.(type) {
	case *ast.Ident:
		return constraint.Name == "any"
	case *ast.InterfaceType:
		return constraint.Methods == nil || len(constraint.Methods.List) == 0
	default:
		return false
	}
}

func parsePluginProduction(t *testing.T) (*token.FileSet, []goSource) {
	t.Helper()
	pluginRoot := filepath.Join(repositoryRoot(t), "plugin")
	fileSet := token.NewFileSet()
	sources := make([]goSource, 0)
	err := filepath.WalkDir(pluginRoot, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		tree, parseErr := parser.ParseFile(fileSet, path, nil, parser.ParseComments)
		if parseErr != nil {
			return parseErr
		}
		if generatedSource(tree) {
			return nil
		}
		sources = append(sources, goSource{
			path: path,
			tree: tree,
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return fileSet, sources
}
