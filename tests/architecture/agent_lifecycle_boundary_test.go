package architecture

import (
	"go/ast"
	"go/token"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

type businessTypeBoundary struct {
	directory string
	packageID string
	typeName  string
}

func TestAgentLifecycleBusinessTypesStayOutsidePluginLifecycle(t *testing.T) {
	t.Parallel()
	repositoryPath := repositoryRoot(t)
	fileSet := token.NewFileSet()
	sourcesByPackage := parsePackages(t, fileSet, repositoryPath)
	boundaries := []businessTypeBoundary{
		{
			directory: "session",
			packageID: "session",
			typeName:  "memoryStore",
		},
		{
			directory: "agent",
			packageID: "agent",
			typeName:  "RegistryService",
		},
		{
			directory: "agent",
			packageID: "agent",
			typeName:  "LifecycleCoordinator",
		},
		{
			directory: "agentloop",
			packageID: "agentloop",
			typeName:  "Factory",
		},
		{
			directory: "agentloop",
			packageID: "agentloop",
			typeName:  "ReactLoopAgent",
		},
		{
			directory: filepath.Join("subagent", "spawn"),
			packageID: "spawn",
			typeName:  "Builder",
		},
		{
			directory: filepath.Join("subagent", "fork"),
			packageID: "fork",
			typeName:  "Builder",
		},
		{
			directory: filepath.Join("subagent", "internal", "seedbuilder"),
			packageID: "seedbuilder",
			typeName:  "Registry",
		},
		{
			directory: filepath.Join("subagent", "internal", "oneshot"),
			packageID: "oneshot",
			typeName:  "Service",
		},
		{
			directory: filepath.Join("subagent", "internal", "continuable"),
			packageID: "continuable",
			typeName:  "Service",
		},
		{
			directory: filepath.Join("subagent", "internal", "subagents"),
			packageID: "subagents",
			typeName:  "Service",
		},
		{
			directory: "userquestions",
			packageID: "userquestions",
			typeName:  "QuestionService",
		},
	}
	forbiddenMethods := map[string]struct{}{
		"Apply":    {},
		"Dispose":  {},
		"Manifest": {},
	}
	findings := make([]string, 0)
	for _, boundary := range boundaries {
		packageKey := filepath.Join(repositoryPath, boundary.directory) + ":" + boundary.packageID
		sources := productionSources(sourcesByPackage[packageKey])
		if len(sources) == 0 {
			findings = append(findings, boundary.typeName+": package sources not found")
			continue
		}
		foundType := false
		for _, source := range sources {
			for _, declaration := range source.tree.Decls {
				switch current := declaration.(type) {
				case *ast.GenDecl:
					for _, specification := range current.Specs {
						typeSpecification, ok := specification.(*ast.TypeSpec)
						if !ok || typeSpecification.Name.Name != boundary.typeName {
							continue
						}
						foundType = true
						structure, ok := typeSpecification.Type.(*ast.StructType)
						if !ok {
							continue
						}
						for _, field := range structure.Fields.List {
							selector, embedded := field.Type.(*ast.SelectorExpr)
							packageName, selectedPackage := selectorPackage(selector)
							if embedded && selectedPackage && packageName == "plugin" &&
								selector.Sel.Name == "Base" {
								findings = append(
									findings,
									fileSet.Position(field.Pos()).String()+": "+boundary.typeName+" embeds plugin.Base",
								)
							}
						}
					}
				case *ast.FuncDecl:
					if current.Recv == nil || len(current.Recv.List) != 1 ||
						receiverTypeName(current.Recv.List[0].Type) != boundary.typeName {
						continue
					}
					if _, forbidden := forbiddenMethods[current.Name.Name]; forbidden {
						findings = append(
							findings,
							fileSet.Position(current.Name.Pos()).String()+": "+boundary.typeName+" implements Plugin lifecycle",
						)
					}
				}
			}
		}
		if !foundType {
			findings = append(findings, boundary.typeName+": type declaration not found")
		}
	}
	if len(findings) == 0 {
		return
	}
	sort.Strings(findings)
	t.Fatalf(
		"Agent lifecycle business objects must stay separate from Plugin lifecycle:\n%s",
		strings.Join(findings, "\n"),
	)
}

func TestAgentScopeRootDelegatesStructuralUnloadToAgentScopes(t *testing.T) {
	t.Parallel()
	repositoryPath := repositoryRoot(t)
	fileSet := token.NewFileSet()
	sourcesByPackage := parsePackages(t, fileSet, repositoryPath)
	packageKey := filepath.Join(repositoryPath, "agentloop") + ":agentloop"
	sources := productionSources(sourcesByPackage[packageKey])
	rootFound := false
	scopesFound := false
	scopesOwnHandle := false
	scopesUnload := false
	findings := make([]string, 0)
	for _, source := range sources {
		for _, declaration := range source.tree.Decls {
			switch current := declaration.(type) {
			case *ast.GenDecl:
				for _, specification := range current.Specs {
					typeSpecification, ok := specification.(*ast.TypeSpec)
					if !ok {
						continue
					}
					structure, ok := typeSpecification.Type.(*ast.StructType)
					if !ok {
						continue
					}
					switch typeSpecification.Name.Name {
					case "agentScopeRoot":
						rootFound = true
						for _, field := range structure.Fields.List {
							ast.Inspect(field.Type, func(node ast.Node) bool {
								selector, ok := node.(*ast.SelectorExpr)
								packageName, selectedPackage := selectorPackage(selector)
								if ok && selectedPackage && packageName == "plugin" &&
									(selector.Sel.Name == "Handle" ||
										selector.Sel.Name == "Plugin") {
									findings = append(
										findings,
										fileSet.Position(selector.Pos()).String()+
											": agentScopeRoot owns plugin."+
											selector.Sel.Name,
									)
								}
								return true
							})
						}
					case "agentScopes":
						scopesFound = true
						for _, field := range structure.Fields.List {
							ast.Inspect(field.Type, func(node ast.Node) bool {
								selector, ok := node.(*ast.SelectorExpr)
								packageName, selectedPackage := selectorPackage(selector)
								if ok && selectedPackage && packageName == "plugin" &&
									selector.Sel.Name == "Handle" {
									scopesOwnHandle = true
								}
								return true
							})
						}
					}
				}
			case *ast.FuncDecl:
				if current.Recv == nil || len(current.Recv.List) != 1 {
					continue
				}
				receiverName := receiverTypeName(current.Recv.List[0].Type)
				ast.Inspect(current.Body, func(node ast.Node) bool {
					call, ok := node.(*ast.CallExpr)
					if !ok {
						return true
					}
					selector, ok := call.Fun.(*ast.SelectorExpr)
					packageName, selectedPackage := selectorPackage(selector)
					if !ok || !selectedPackage || packageName != "plugin" ||
						selector.Sel.Name != "UnloadChild" {
						return true
					}
					if receiverName == "agentScopeRoot" {
						findings = append(
							findings,
							fileSet.Position(selector.Pos()).String()+
								": agentScopeRoot unloads itself",
						)
					}
					if receiverName == "agentScopes" && current.Name.Name == "release" {
						scopesUnload = true
					}
					return true
				})
			}
		}
	}
	if !rootFound {
		findings = append(findings, "agentScopeRoot type declaration not found")
	}
	if !scopesFound {
		findings = append(findings, "agentScopes type declaration not found")
	}
	if !scopesOwnHandle {
		findings = append(findings, "agentScopes does not own plugin.Handle")
	}
	if !scopesUnload {
		findings = append(findings, "agentScopes.release does not issue plugin.UnloadChild")
	}
	if len(findings) == 0 {
		return
	}
	sort.Strings(findings)
	t.Fatalf(
		"Agent Scope structural ownership is invalid:\n%s",
		strings.Join(findings, "\n"),
	)
}

func TestAgentLifecycleBusinessFilesDoNotCallPluginRuntime(t *testing.T) {
	t.Parallel()
	repositoryPath := repositoryRoot(t)
	fileSet := token.NewFileSet()
	sourcesByPackage := parsePackages(t, fileSet, repositoryPath)
	files := map[string][]string{
		filepath.Join(repositoryPath, "session") + ":session": {
			"memory_store.go",
			"registration.go",
		},
		filepath.Join(repositoryPath, "agent") + ":agent": {
			"lifecycle_coordinator.go",
			"registry.go",
		},
		filepath.Join(repositoryPath, "agentloop") + ":agentloop": {
			"agent.go",
			"construction.go",
		},
		filepath.Join(repositoryPath, "subagent", "spawn") + ":spawn": {
			"provider.go",
		},
		filepath.Join(repositoryPath, "subagent", "fork") + ":fork": {
			"provider.go",
		},
	}
	forbidden := map[string]struct{}{
		"Publish":      {},
		"PublishEvent": {},
		"Require":      {},
		"Resolve":      {},
	}
	findings := make([]string, 0)
	for packageKey, selectedFiles := range files {
		selected := make(map[string]struct{}, len(selectedFiles))
		for _, fileName := range selectedFiles {
			selected[fileName] = struct{}{}
		}
		for _, source := range productionSources(sourcesByPackage[packageKey]) {
			if _, included := selected[filepath.Base(source.path)]; !included {
				continue
			}
			ast.Inspect(source.tree, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				selector, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				packageName, selectedPackage := selectorPackage(selector)
				if !selectedPackage || packageName != "plugin" {
					return true
				}
				if _, blocked := forbidden[selector.Sel.Name]; blocked {
					findings = append(
						findings,
						fileSet.Position(selector.Pos()).String()+": plugin."+selector.Sel.Name,
					)
				}
				return true
			})
		}
	}
	if len(findings) == 0 {
		return
	}
	sort.Strings(findings)
	t.Fatalf(
		"Agent lifecycle business files must use injected ports, not Plugin Runtime APIs:\n%s",
		strings.Join(findings, "\n"),
	)
}

func TestSessionCoreDoesNotActivateAgents(t *testing.T) {
	t.Parallel()
	repositoryPath := repositoryRoot(t)
	fileSet := token.NewFileSet()
	sourcesByPackage := parsePackages(t, fileSet, repositoryPath)
	sessionSources := productionSources(
		sourcesByPackage[filepath.Join(repositoryPath, "session")+":session"],
	)
	findings := make([]string, 0)
	for _, source := range sessionSources {
		for _, imported := range source.tree.Imports {
			importPath, err := strconv.Unquote(imported.Path.Value)
			if err != nil {
				t.Fatal(err)
			}
			if importPath == "github.com/gorenx/goren/agent" ||
				importPath == "github.com/gorenx/goren/agentloop" ||
				strings.HasPrefix(importPath, "github.com/gorenx/goren/subagent") {
				findings = append(
					findings,
					fileSet.Position(imported.Pos()).String()+": "+importPath,
				)
			}
		}
	}
	if len(findings) == 0 {
		return
	}
	sort.Strings(findings)
	t.Fatalf(
		"Session core must not create, resume, or classify Agents:\n%s",
		strings.Join(findings, "\n"),
	)
}

func TestRemovedAgentLifecycleSymbolsStayAbsent(t *testing.T) {
	t.Parallel()
	repositoryPath := repositoryRoot(t)
	fileSet := token.NewFileSet()
	sourcesByPackage := parsePackages(t, fileSet, repositoryPath)
	targetDirectories := []string{
		"session",
		"agent",
		"agentloop",
		"subagent",
		filepath.Join("apiproxy", "session"),
		"userquestions",
	}
	forbiddenIdentifiers := map[string]struct{}{
		"BeginShutdown":               {},
		"CloseAll":                    {},
		"CloseDescendants":            {},
		"Custody":                     {},
		"DescendantLifecycle":         {},
		"DrainChildren":               {},
		"DrainContinuableChildren":    {},
		"DrainContinuableDescendants": {},
		"DrainDescendants":            {},
		"HandleTransferred":           {},
		"IsOwnedBy":                   {},
		"Roots":                       {},
		"SubagentDepth":               {},
		"closingRoots":                {},
		"custodyFrom":                 {},
		"ownedChildren":               {},
		"questionRegistry":            {},
	}
	findings := make([]string, 0)
	for packageKey, sources := range sourcesByPackage {
		packageDirectory := strings.SplitN(packageKey, ":", 2)[0]
		if !withinAnyDirectory(repositoryPath, packageDirectory, targetDirectories) {
			continue
		}
		for _, source := range productionSources(sources) {
			ast.Inspect(source.tree, func(node ast.Node) bool {
				switch current := node.(type) {
				case *ast.Ident:
					if _, forbidden := forbiddenIdentifiers[current.Name]; forbidden {
						findings = append(
							findings,
							fileSet.Position(current.Pos()).String()+": "+current.Name,
						)
					}
				case *ast.BasicLit:
					if current.Kind != token.STRING {
						return true
					}
					literal, err := strconv.Unquote(current.Value)
					if err == nil && strings.Contains(literal, "session/prepared") {
						findings = append(
							findings,
							fileSet.Position(current.Pos()).String()+": session/prepared",
						)
					}
				}
				return true
			})
		}
	}
	if len(findings) == 0 {
		return
	}
	sort.Strings(findings)
	t.Fatalf(
		"removed Agent lifecycle symbols were reintroduced:\n%s",
		strings.Join(findings, "\n"),
	)
}

func productionSources(sources []goSource) []goSource {
	result := make([]goSource, 0, len(sources))
	for _, source := range sources {
		if strings.HasSuffix(source.path, "_test.go") {
			continue
		}
		result = append(result, source)
	}
	return result
}

func selectorPackage(selector *ast.SelectorExpr) (string, bool) {
	if selector == nil {
		return "", false
	}
	identifier, ok := selector.X.(*ast.Ident)
	if !ok {
		return "", false
	}
	return identifier.Name, true
}

func withinAnyDirectory(
	repositoryPath string,
	packageDirectory string,
	targetDirectories []string,
) bool {
	for _, relativeDirectory := range targetDirectories {
		target := filepath.Join(repositoryPath, relativeDirectory)
		if packageDirectory == target ||
			strings.HasPrefix(packageDirectory, target+string(filepath.Separator)) {
			return true
		}
	}
	return false
}
