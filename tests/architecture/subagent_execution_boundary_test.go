package architecture

import (
	"go/ast"
	"go/token"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func TestSubagentModesOwnIndependentExecutionState(t *testing.T) {
	t.Parallel()
	repositoryPath := repositoryRoot(t)
	fileSet := token.NewFileSet()
	sourcesByPackage := parsePackages(t, fileSet, repositoryPath)
	modeTypes := map[string]string{
		"bound":       "residentEpoch",
		"continuable": "residentExecution",
		"oneshot":     "oneShotExecution",
	}
	requiredMethods := map[string]struct{}{
		"Activate":    {},
		"ChildID":     {},
		"Dispose":     {},
		"Result":      {},
		"RunID":       {},
		"State":       {},
		"Stop":        {},
		"StopAndWait": {},
		"Wait":        {},
	}
	findings := make([]string, 0)
	for packageName, typeName := range modeTypes {
		packageKey := filepath.Join(
			repositoryPath,
			"subagent",
			"internal",
			packageName,
		) + ":" + packageName
		methods := make(map[string]struct{})
		foundType := false
		for _, source := range productionSources(sourcesByPackage[packageKey]) {
			for _, declaration := range source.tree.Decls {
				switch current := declaration.(type) {
				case *ast.GenDecl:
					for _, specification := range current.Specs {
						typeSpecification, ok := specification.(*ast.TypeSpec)
						if ok && typeSpecification.Name.Name == typeName {
							foundType = true
						}
					}
				case *ast.FuncDecl:
					if current.Recv != nil && len(current.Recv.List) == 1 &&
						receiverTypeName(current.Recv.List[0].Type) == typeName {
						methods[current.Name.Name] = struct{}{}
					}
				}
			}
		}
		if !foundType {
			findings = append(findings, packageName+": "+typeName+" is missing")
			continue
		}
		for methodName := range requiredMethods {
			if _, found := methods[methodName]; !found {
				findings = append(
					findings,
					packageName+": "+typeName+" does not own "+methodName,
				)
			}
		}
	}

	executionKey := filepath.Join(
		repositoryPath,
		"subagent",
		"internal",
		"execution",
	) + ":execution"
	for _, source := range productionSources(sourcesByPackage[executionKey]) {
		for _, declaration := range source.tree.Decls {
			group, ok := declaration.(*ast.GenDecl)
			if !ok {
				continue
			}
			for _, specification := range group.Specs {
				typeSpecification, ok := specification.(*ast.TypeSpec)
				if !ok {
					continue
				}
				if typeSpecification.Name.Name == "Execution" ||
					typeSpecification.Name.Name == "Terminator" {
					findings = append(
						findings,
						fileSet.Position(typeSpecification.Pos()).String()+
							": shared lifecycle owner "+typeSpecification.Name.Name,
					)
				}
			}
		}
	}
	if len(findings) == 0 {
		return
	}
	sort.Strings(findings)
	t.Fatalf(
		"each Subagent mode must own its state machine:\n%s",
		strings.Join(findings, "\n"),
	)
}

func TestSubagentExecutionsDoNotRetainChildOwners(t *testing.T) {
	t.Parallel()
	repositoryPath := repositoryRoot(t)
	fileSet := token.NewFileSet()
	sourcesByPackage := parsePackages(t, fileSet, repositoryPath)
	forbidden := map[string]string{
		filepath.Join(repositoryPath, "subagent", "internal", "bound") + ":bound":             "boundChild",
		filepath.Join(repositoryPath, "subagent", "internal", "continuable") + ":continuable": "continuableChild",
	}
	findings := make([]string, 0)
	for packageKey, forbiddenType := range forbidden {
		for _, source := range productionSources(sourcesByPackage[packageKey]) {
			for _, declaration := range source.tree.Decls {
				group, ok := declaration.(*ast.GenDecl)
				if !ok {
					continue
				}
				for _, specification := range group.Specs {
					typeSpecification, ok := specification.(*ast.TypeSpec)
					if !ok {
						continue
					}
					if typeSpecification.Name.Name != "residentEpoch" &&
						typeSpecification.Name.Name != "residentExecution" {
						continue
					}
					structure, ok := typeSpecification.Type.(*ast.StructType)
					if !ok {
						continue
					}
					for _, field := range structure.Fields.List {
						ast.Inspect(field.Type, func(node ast.Node) bool {
							identifier, ok := node.(*ast.Ident)
							if ok && identifier.Name == forbiddenType {
								findings = append(
									findings,
									fileSet.Position(identifier.Pos()).String()+
										": resident retains "+forbiddenType,
								)
							}
							return true
						})
					}
				}
			}
		}
	}
	if len(findings) == 0 {
		return
	}
	sort.Strings(findings)
	t.Fatalf(
		"Subagent execution ownership must remain acyclic:\n%s",
		strings.Join(findings, "\n"),
	)
}
