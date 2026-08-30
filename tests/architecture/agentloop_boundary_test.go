package architecture

import (
	"go/ast"
	"go/token"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
)

func TestAgentLoopStateMachinesStayPureAndIndependent(t *testing.T) {
	t.Parallel()
	repositoryPath := repositoryRoot(t)
	fileSet := token.NewFileSet()
	sourcesByPackage := parsePackages(t, fileSet, repositoryPath)
	packages := []string{
		"execution",
		"lifecycle",
		"modelrequest",
		"step",
		"toolbatch",
		"turn",
	}
	findings := make([]string, 0)
	for _, packageName := range packages {
		packageDirectory := filepath.Join(
			repositoryPath,
			"agentloop",
			"internal",
			packageName,
		)
		packageKey := packageDirectory + ":" + packageName
		sources := productionSources(sourcesByPackage[packageKey])
		if len(sources) == 0 {
			findings = append(findings, packageName+": production package is missing")
			continue
		}
		for _, source := range sources {
			for _, imported := range source.tree.Imports {
				importPath, err := strconv.Unquote(imported.Path.Value)
				if err != nil {
					t.Fatal(err)
				}
				if strings.HasPrefix(importPath, "github.com/gorenx/goren/") {
					findings = append(
						findings,
						fileSet.Position(imported.Pos()).String()+": "+importPath,
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
		"AgentLoop state machines must not import effectful or peer packages:\n%s",
		strings.Join(findings, "\n"),
	)
}

func TestRemovedAgentLoopSymbolsStayAbsent(t *testing.T) {
	t.Parallel()
	repositoryPath := repositoryRoot(t)
	fileSet := token.NewFileSet()
	sourcesByPackage := parsePackages(t, fileSet, repositoryPath)
	forbidden := map[string]struct{}{
		"activityCoordinator":       {},
		"activityKind":              {},
		"activityPosition":          {},
		"activityState":             {},
		"agentEventPublisher":       {},
		"agentRuntimeAdapter":       {},
		"agentScopeRoot":            {},
		"agentScopes":               {},
		"configuredAgentStarter":    {},
		"newAgentScopeRoot":         {},
		"newAgentScopes":            {},
		"newConfiguredAgentStarter": {},
		"newLoop":                   {},
		"observerFailureReporter":   {},
		"runtimeContextProjection":  {},
		"runtimeContextRouter":      {},
		"stepExecutor":              {},
		"stepPlan":                  {},
		"toolCallExecutor":          {},
		"turnRunner":                {},
	}
	findings := make([]string, 0)
	for packageKey, sources := range sourcesByPackage {
		packageDirectory := strings.SplitN(packageKey, ":", 2)[0]
		if packageDirectory != filepath.Join(repositoryPath, "agentloop") &&
			!strings.HasPrefix(
				packageDirectory,
				filepath.Join(repositoryPath, "agentloop")+string(filepath.Separator),
			) {
			continue
		}
		for _, source := range productionSources(sources) {
			ast.Inspect(source.tree, func(node ast.Node) bool {
				identifier, ok := node.(*ast.Ident)
				if !ok {
					return true
				}
				if _, blocked := forbidden[identifier.Name]; blocked {
					findings = append(
						findings,
						fileSet.Position(identifier.Pos()).String()+": "+identifier.Name,
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
		"removed AgentLoop symbols were reintroduced:\n%s",
		strings.Join(findings, "\n"),
	)
}

func TestAgentLoopConcreteStructOwnershipIsAllowedAndAcyclic(t *testing.T) {
	t.Parallel()
	repositoryPath := repositoryRoot(t)
	fileSet := token.NewFileSet()
	sourcesByPackage := parsePackages(t, fileSet, repositoryPath)
	packageKey := filepath.Join(repositoryPath, "agentloop") + ":agentloop"
	sources := productionSources(sourcesByPackage[packageKey])
	structs := make(map[string]struct{})
	for _, source := range sources {
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
				if _, ok = typeSpecification.Type.(*ast.StructType); ok {
					structs[typeSpecification.Name.Name] = struct{}{}
				}
			}
		}
	}
	edges := make(map[string]map[string]struct{})
	positions := make(map[string]string)
	for _, source := range sources {
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
				structure, ok := typeSpecification.Type.(*ast.StructType)
				if !ok {
					continue
				}
				sourceName := typeSpecification.Name.Name
				for _, field := range structure.Fields.List {
					ast.Inspect(field.Type, func(node ast.Node) bool {
						identifier, ok := node.(*ast.Ident)
						if !ok || identifier.Name == sourceName {
							return true
						}
						if _, local := structs[identifier.Name]; !local {
							return true
						}
						if edges[sourceName] == nil {
							edges[sourceName] = make(map[string]struct{})
						}
						edges[sourceName][identifier.Name] = struct{}{}
						positions[sourceName+" -> "+identifier.Name] =
							fileSet.Position(identifier.Pos()).String()
						return true
					})
				}
			}
		}
	}
	allowed := map[string]struct{}{
		"Factory -> factoryDependencies":                       {},
		"Plugin -> Factory":                                    {},
		"Plugin -> pluginGateway":                              {},
		"Settings -> StartupAgent":                             {},
		"StartupPlan -> StartupAgent":                          {},
		"agentHost -> ReactLoopAgent":                          {},
		"agentHost -> agentScope":                              {},
		"agentScope -> scopeRegistration":                      {},
		"agentScope -> scopeResources":                         {},
		"agentVariablesSetup -> namedAgentVariable":            {},
		"namedAgentVariable -> staticAgentVariable":            {},
		"preStepCall -> preStepCallResult":                     {},
		"requestErrorCall -> requestErrorCallResult":           {},
		"requestResolutionCall -> requestResolutionCallResult": {},
		"scopeDraft -> agentScope":                             {},
		"toolDispatchResult -> settledToolCall":                {},
		"toolGroupExecution -> plannedToolCall":                {},
		"toolGroupExecution -> settledToolCall":                {},
		"toolGroupExecution -> toolDispatchResult":             {},
	}
	findings := make([]string, 0)
	for sourceName, destinations := range edges {
		for destination := range destinations {
			edge := sourceName + " -> " + destination
			if _, accepted := allowed[edge]; !accepted {
				findings = append(findings, positions[edge]+": undeclared edge "+edge)
			}
		}
	}
	findings = append(findings, concreteStructCycles(edges)...)
	if len(findings) == 0 {
		return
	}
	sort.Strings(findings)
	t.Fatalf(
		"AgentLoop concrete struct ownership must match the declared DAG:\n%s",
		strings.Join(findings, "\n"),
	)
}

func concreteStructCycles(edges map[string]map[string]struct{}) []string {
	const (
		unseen uint8 = iota
		visiting
		visited
	)
	states := make(map[string]uint8)
	path := make([]string, 0)
	findings := make([]string, 0)
	var visit func(string)
	visit = func(source string) {
		states[source] = visiting
		path = append(path, source)
		destinations := make([]string, 0, len(edges[source]))
		for destination := range edges[source] {
			destinations = append(destinations, destination)
		}
		sort.Strings(destinations)
		for _, destination := range destinations {
			switch states[destination] {
			case unseen:
				visit(destination)
			case visiting:
				start := 0
				for path[start] != destination {
					start++
				}
				cycle := append([]string(nil), path[start:]...)
				cycle = append(cycle, destination)
				findings = append(
					findings,
					"ownership cycle: "+strings.Join(cycle, " -> "),
				)
			}
		}
		path = path[:len(path)-1]
		states[source] = visited
	}
	nodes := make([]string, 0, len(edges))
	for source := range edges {
		nodes = append(nodes, source)
	}
	sort.Strings(nodes)
	for _, source := range nodes {
		if states[source] == unseen {
			visit(source)
		}
	}
	return findings
}

func TestReactLoopAgentMethodsUseRLAFilenames(t *testing.T) {
	t.Parallel()
	repositoryPath := repositoryRoot(t)
	fileSet := token.NewFileSet()
	sourcesByPackage := parsePackages(t, fileSet, repositoryPath)
	packageKey := filepath.Join(repositoryPath, "agentloop") + ":agentloop"
	findings := make([]string, 0)
	for _, source := range productionSources(sourcesByPackage[packageKey]) {
		for _, declaration := range source.tree.Decls {
			method, ok := declaration.(*ast.FuncDecl)
			if !ok || method.Recv == nil || len(method.Recv.List) != 1 ||
				receiverTypeName(method.Recv.List[0].Type) != "ReactLoopAgent" {
				continue
			}
			if !strings.HasPrefix(filepath.Base(source.path), "rla") {
				findings = append(
					findings,
					fileSet.Position(method.Name.Pos()).String()+": "+method.Name.Name,
				)
			}
		}
	}
	if len(findings) == 0 {
		return
	}
	sort.Strings(findings)
	t.Fatalf(
		"ReactLoopAgent methods must use the rla filename prefix:\n%s",
		strings.Join(findings, "\n"),
	)
}

func TestRLAFilesDoNotDependOnPluginRuntime(t *testing.T) {
	t.Parallel()
	repositoryPath := repositoryRoot(t)
	fileSet := token.NewFileSet()
	sourcesByPackage := parsePackages(t, fileSet, repositoryPath)
	packageKey := filepath.Join(repositoryPath, "agentloop") + ":agentloop"
	findings := make([]string, 0)
	for _, source := range productionSources(sourcesByPackage[packageKey]) {
		if !strings.HasPrefix(filepath.Base(source.path), "rla") &&
			filepath.Base(source.path) != "tool_group_execution.go" {
			continue
		}
		for _, imported := range source.tree.Imports {
			importPath, err := strconv.Unquote(imported.Path.Value)
			if err != nil {
				t.Fatal(err)
			}
			if importPath == "github.com/gorenx/goren/plugin" ||
				strings.HasPrefix(importPath, "github.com/gorenx/goren/plugin/") {
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
		"RLA business orchestration must not import Plugin Runtime:\n%s",
		strings.Join(findings, "\n"),
	)
}

func TestAgentLoopPluginImplementationsUsePluginFilenames(t *testing.T) {
	t.Parallel()
	repositoryPath := repositoryRoot(t)
	fileSet := token.NewFileSet()
	sourcesByPackage := parsePackages(t, fileSet, repositoryPath)
	packageKey := filepath.Join(repositoryPath, "agentloop") + ":agentloop"
	pluginMethods := map[string]struct{}{
		"Apply":    {},
		"Dispose":  {},
		"Manifest": {},
	}
	findings := make([]string, 0)
	pluginReceivers := make(map[string]struct{})
	for _, source := range productionSources(sourcesByPackage[packageKey]) {
		for _, declaration := range source.tree.Decls {
			method, ok := declaration.(*ast.FuncDecl)
			if !ok || method.Recv == nil || len(method.Recv.List) != 1 ||
				method.Name.Name != "Manifest" {
				continue
			}
			pluginReceivers[receiverTypeName(method.Recv.List[0].Type)] = struct{}{}
		}
	}
	for _, source := range productionSources(sourcesByPackage[packageKey]) {
		for _, declaration := range source.tree.Decls {
			method, ok := declaration.(*ast.FuncDecl)
			if !ok || method.Recv == nil || len(method.Recv.List) != 1 {
				continue
			}
			if _, lifecycleMethod := pluginMethods[method.Name.Name]; !lifecycleMethod {
				continue
			}
			receiverName := receiverTypeName(method.Recv.List[0].Type)
			if _, isPlugin := pluginReceivers[receiverName]; !isPlugin {
				continue
			}
			if !strings.HasPrefix(filepath.Base(source.path), "plugin") {
				findings = append(
					findings,
					fileSet.Position(method.Name.Pos()).String()+": "+
						receiverName+"."+
						method.Name.Name,
				)
			}
		}
	}
	if len(findings) == 0 {
		return
	}
	sort.Strings(findings)
	t.Fatalf(
		"AgentLoop Plugin implementation files must start with plugin:\n%s",
		strings.Join(findings, "\n"),
	)
}

func TestReactLoopAgentShutdownBelongsToAgentHost(t *testing.T) {
	t.Parallel()
	repositoryPath := repositoryRoot(t)
	fileSet := token.NewFileSet()
	sourcesByPackage := parsePackages(t, fileSet, repositoryPath)
	packageKey := filepath.Join(repositoryPath, "agentloop") + ":agentloop"
	callers := make([]string, 0)
	for _, source := range productionSources(sourcesByPackage[packageKey]) {
		for _, declaration := range source.tree.Decls {
			method, ok := declaration.(*ast.FuncDecl)
			if !ok || method.Recv == nil || len(method.Recv.List) != 1 {
				continue
			}
			ast.Inspect(method.Body, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				selector, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || selector.Sel.Name != "shutdown" {
					return true
				}
				callers = append(
					callers,
					receiverTypeName(method.Recv.List[0].Type)+"."+method.Name.Name,
				)
				return true
			})
		}
	}
	want := []string{"agentHost.Close"}
	if slices.Equal(callers, want) {
		return
	}
	t.Fatalf("ReactLoopAgent shutdown callers = %#v, want %#v", callers, want)
}

func TestAgentLoopPluginDoesNotOwnAgentInstances(t *testing.T) {
	t.Parallel()
	repositoryPath := repositoryRoot(t)
	fileSet := token.NewFileSet()
	sourcesByPackage := parsePackages(t, fileSet, repositoryPath)
	packageKey := filepath.Join(repositoryPath, "agentloop") + ":agentloop"
	findings := make([]string, 0)
	for _, source := range productionSources(sourcesByPackage[packageKey]) {
		for _, declaration := range source.tree.Decls {
			group, ok := declaration.(*ast.GenDecl)
			if !ok {
				continue
			}
			for _, specification := range group.Specs {
				typeSpecification, ok := specification.(*ast.TypeSpec)
				if !ok || typeSpecification.Name.Name != "Plugin" {
					continue
				}
				structure, ok := typeSpecification.Type.(*ast.StructType)
				if !ok {
					continue
				}
				for _, field := range structure.Fields.List {
					ast.Inspect(field.Type, func(node ast.Node) bool {
						switch current := node.(type) {
						case *ast.MapType:
							ast.Inspect(current, func(nested ast.Node) bool {
								identifier, matches := nested.(*ast.Ident)
								if matches && identifier.Name == "ReactLoopAgent" {
									findings = append(
										findings,
										fileSet.Position(current.Pos()).String()+
											": AgentLoop Plugin retains an Agent map",
									)
								}
								return true
							})
						case *ast.Ident:
							if current.Name == "ReactLoopAgent" {
								findings = append(
									findings,
									fileSet.Position(current.Pos()).String()+
										": AgentLoop Plugin retains an Agent",
								)
							}
						}
						return true
					})
				}
			}
		}
	}
	if len(findings) == 0 {
		return
	}
	sort.Strings(findings)
	t.Fatalf(
		"AgentLoop Plugin must not own Agent instances:\n%s",
		strings.Join(findings, "\n"),
	)
}

func TestAgentLoopKeyedStructLiteralsUseOneFieldPerLine(t *testing.T) {
	t.Parallel()
	repositoryPath := repositoryRoot(t)
	fileSet := token.NewFileSet()
	sourcesByPackage := parsePackages(t, fileSet, repositoryPath)
	agentLoopPath := filepath.Join(repositoryPath, "agentloop")
	findings := make([]string, 0)
	for packageKey, sources := range sourcesByPackage {
		packageDirectory := strings.SplitN(packageKey, ":", 2)[0]
		if packageDirectory != agentLoopPath &&
			!strings.HasPrefix(
				packageDirectory,
				agentLoopPath+string(filepath.Separator),
			) {
			continue
		}
		for _, source := range productionSources(sources) {
			ast.Inspect(source.tree, func(node ast.Node) bool {
				literal, ok := node.(*ast.CompositeLit)
				if !ok || len(literal.Elts) < 2 {
					return true
				}
				if _, mapLiteral := literal.Type.(*ast.MapType); mapLiteral {
					return true
				}
				fieldLines := make(map[int]string)
				for _, element := range literal.Elts {
					assignment, keyed := element.(*ast.KeyValueExpr)
					if !keyed {
						return true
					}
					field, named := assignment.Key.(*ast.Ident)
					if !named {
						return true
					}
					line := fileSet.Position(assignment.Pos()).Line
					if prior, occupied := fieldLines[line]; occupied {
						findings = append(
							findings,
							fileSet.Position(assignment.Pos()).String()+": "+
								prior+" and "+field.Name,
						)
					}
					fieldLines[line] = field.Name
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
		"AgentLoop keyed struct literals must place one field on each line:\n%s",
		strings.Join(findings, "\n"),
	)
}

func TestAgentLoopConstantsAreDocumentedIndividually(t *testing.T) {
	t.Parallel()
	repositoryPath := repositoryRoot(t)
	fileSet := token.NewFileSet()
	sourcesByPackage := parsePackages(t, fileSet, repositoryPath)
	agentLoopPath := filepath.Join(repositoryPath, "agentloop")
	findings := make([]string, 0)
	for packageKey, sources := range sourcesByPackage {
		packageDirectory := strings.SplitN(packageKey, ":", 2)[0]
		if packageDirectory != agentLoopPath &&
			!strings.HasPrefix(
				packageDirectory,
				agentLoopPath+string(filepath.Separator),
			) {
			continue
		}
		for _, source := range productionSources(sources) {
			for _, declaration := range source.tree.Decls {
				group, ok := declaration.(*ast.GenDecl)
				if !ok || group.Tok != token.CONST {
					continue
				}
				for _, specification := range group.Specs {
					value, ok := specification.(*ast.ValueSpec)
					if !ok || value.Doc != nil ||
						(len(group.Specs) == 1 && group.Doc != nil) {
						continue
					}
					for _, identifier := range value.Names {
						findings = append(
							findings,
							fileSet.Position(identifier.Pos()).String()+": "+
								identifier.Name,
						)
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
		"AgentLoop constants must each explain their meaning:\n%s",
		strings.Join(findings, "\n"),
	)
}
