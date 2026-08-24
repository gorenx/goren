package architecture

import (
	"go/ast"
	"go/token"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func TestSessionCoordinatorExportsOnlyContextContract(t *testing.T) {
	t.Parallel()
	fileSet := token.NewFileSet()
	repositoryPath := repositoryRoot(t)
	sourcesByPackage := parsePackages(t, fileSet, repositoryPath)
	sessionSources := sourcesByPackage[filepath.Join(repositoryPath, "session")+":session"]
	if len(sessionSources) == 0 {
		t.Fatal("session package sources not found")
	}

	allowedMethods := map[string]struct{}{
		"Commit":         {},
		"DeriveMessages": {},
		"Events":         {},
		"FirstLiveSeq":   {},
		"Header":         {},
		"ID":             {},
		"Seq":            {},
		"Snapshot":       {},
		"Surface":        {},
	}
	foundMethods := map[string]struct{}{}
	findings := []string{}
	for _, source := range sessionSources {
		for _, declaration := range source.tree.Decls {
			functionDeclaration, ok := declaration.(*ast.FuncDecl)
			if !ok || functionDeclaration.Recv == nil ||
				len(functionDeclaration.Recv.List) != 1 ||
				receiverTypeName(functionDeclaration.Recv.List[0].Type) != "coordinator" ||
				!functionDeclaration.Name.IsExported() {
				continue
			}
			methodName := functionDeclaration.Name.Name
			if _, allowed := allowedMethods[methodName]; !allowed {
				position := fileSet.Position(functionDeclaration.Name.Pos())
				findings = append(findings, position.String()+": exported coordinator method "+methodName)
				continue
			}
			foundMethods[methodName] = struct{}{}
		}
	}
	for methodName := range allowedMethods {
		if _, found := foundMethods[methodName]; !found {
			findings = append(findings, "missing Context method "+methodName)
		}
	}
	if len(findings) != 0 {
		sort.Strings(findings)
		t.Fatalf("Session concrete context must expose exactly the Context contract:\n%s", strings.Join(findings, "\n"))
	}
}

func TestRemovedSessionWriteAPIsStayAbsent(t *testing.T) {
	t.Parallel()
	fileSet := token.NewFileSet()
	repositoryPath := repositoryRoot(t)
	sourcesByPackage := parsePackages(t, fileSet, repositoryPath)
	sessionSources := sourcesByPackage[filepath.Join(repositoryPath, "session")+":session"]
	if len(sessionSources) == 0 {
		t.Fatal("session package sources not found")
	}

	removedNames := map[string]struct{}{
		"AfterEvent":              {},
		"Append":                  {},
		"AppendOptions":           {},
		"AppendSerialized":        {},
		"AppendSurface":           {},
		"AppendSurfaceSerialized": {},
		"DeferAfterEvent":         {},
		"ExecuteWrite":            {},
		"SerializeProducer":       {},
		"WriteContext":            {},
	}
	removedTypes := map[string]struct{}{
		"Session": {},
	}
	findings := []string{}
	for _, source := range sessionSources {
		for _, declaration := range source.tree.Decls {
			switch current := declaration.(type) {
			case *ast.FuncDecl:
				if _, removed := removedNames[current.Name.Name]; removed {
					findings = append(findings, fileSet.Position(current.Name.Pos()).String())
				}
			case *ast.GenDecl:
				for _, specification := range current.Specs {
					typeSpecification, ok := specification.(*ast.TypeSpec)
					if !ok {
						continue
					}
					if _, removed := removedNames[typeSpecification.Name.Name]; removed {
						findings = append(findings, fileSet.Position(typeSpecification.Name.Pos()).String())
					}
					if _, removed := removedTypes[typeSpecification.Name.Name]; removed {
						findings = append(findings, fileSet.Position(typeSpecification.Name.Pos()).String())
					}
				}
			}
		}
	}
	if len(findings) != 0 {
		sort.Strings(findings)
		t.Fatalf("removed Session write APIs were reintroduced:\n%s", strings.Join(findings, "\n"))
	}
}

func receiverTypeName(receiver ast.Expr) string {
	switch current := receiver.(type) {
	case *ast.Ident:
		return current.Name
	case *ast.StarExpr:
		return receiverTypeName(current.X)
	default:
		return ""
	}
}
