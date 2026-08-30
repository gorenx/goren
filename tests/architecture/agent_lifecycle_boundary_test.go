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

func TestAgentContractsStayPluginNeutral(t *testing.T) {
	t.Parallel()
	repositoryPath := repositoryRoot(t)
	fileSet := token.NewFileSet()
	sourcesByPackage := parsePackages(t, fileSet, repositoryPath)
	packageKey := filepath.Join(repositoryPath, "agent") + ":agent"
	sources := productionSources(sourcesByPackage[packageKey])
	findings := make([]string, 0)
	for _, source := range sources {
		baseName := filepath.Base(source.path)
		if baseName != "events.go" && baseName != "scope.go" &&
			baseName != "setup.go" {
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
		"Agent event, Scope, and Setup contracts must not depend on Plugin Runtime:\n%s",
		strings.Join(findings, "\n"),
	)
}

func TestAgentLifetimeDoesNotOwnScopeOrPeerLifetimes(t *testing.T) {
	t.Parallel()
	repositoryPath := repositoryRoot(t)
	fileSet := token.NewFileSet()
	sourcesByPackage := parsePackages(t, fileSet, repositoryPath)
	packageKey := filepath.Join(repositoryPath, "agent") + ":agent"
	sources := productionSources(sourcesByPackage[packageKey])
	found := false
	hostFields := 0
	stateFields := 0
	findings := make([]string, 0)
	for _, source := range sources {
		for _, declaration := range source.tree.Decls {
			group, ok := declaration.(*ast.GenDecl)
			if !ok {
				continue
			}
			for _, specification := range group.Specs {
				typeSpecification, ok := specification.(*ast.TypeSpec)
				if !ok || typeSpecification.Name.Name != "agentLifetime" {
					continue
				}
				found = true
				if filepath.Base(source.path) != "agent_lifetime.go" {
					findings = append(
						findings,
						fileSet.Position(typeSpecification.Pos()).String()+": agentLifetime must be declared in agent_lifetime.go",
					)
				}
				structure, ok := typeSpecification.Type.(*ast.StructType)
				if !ok {
					findings = append(findings, "agentLifetime is not a struct")
					continue
				}
				for _, field := range structure.Fields.List {
					ast.Inspect(field.Type, func(node ast.Node) bool {
						identifier, ok := node.(*ast.Ident)
						if !ok {
							return true
						}
						if identifier.Name == "Host" {
							hostFields++
							return true
						}
						if identifier.Name == "agentLifetimeState" {
							stateFields++
							return true
						}
						if identifier.Name != "Agent" &&
							identifier.Name != "Scope" &&
							identifier.Name != "agentLifetime" {
							return true
						}
						findings = append(
							findings,
							fileSet.Position(identifier.Pos()).String()+": "+identifier.Name,
						)
						return true
					})
				}
			}
		}
	}
	if !found {
		findings = append(findings, "agentLifetime declaration not found")
	} else if hostFields != 1 {
		findings = append(findings, "agentLifetime must contain exactly one Host field")
	}
	if found && stateFields != 1 {
		findings = append(
			findings,
			"agentLifetime must contain exactly one agentLifetimeState field",
		)
	}
	if len(findings) == 0 {
		return
	}
	sort.Strings(findings)
	t.Fatalf(
		"Agent lifetime must own one state and one Host, not Agent, Scope, or peer lifetimes:\n%s",
		strings.Join(findings, "\n"),
	)
}

func TestRegistryDoesNotReadAgentLifetimePrivateState(t *testing.T) {
	t.Parallel()
	repositoryPath := repositoryRoot(t)
	fileSet := token.NewFileSet()
	sourcesByPackage := parsePackages(t, fileSet, repositoryPath)
	packageKey := filepath.Join(repositoryPath, "agent") + ":agent"
	selectedFiles := map[string]struct{}{
		"handle.go":             {},
		"registry.go":           {},
		"registry_lifecycle.go": {},
	}
	privateFields := map[string]struct{}{
		"closeErr":   {},
		"dispatches": {},
		"ownedHost":  {},
		"state":      {},
	}
	findings := make([]string, 0)
	for _, source := range productionSources(sourcesByPackage[packageKey]) {
		if _, selected := selectedFiles[filepath.Base(source.path)]; !selected {
			continue
		}
		ast.Inspect(source.tree, func(node ast.Node) bool {
			selector, ok := node.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if _, private := privateFields[selector.Sel.Name]; !private {
				return true
			}
			findings = append(
				findings,
				fileSet.Position(selector.Pos()).String()+": "+selector.Sel.Name,
			)
			return true
		})
	}
	if len(findings) == 0 {
		return
	}
	sort.Strings(findings)
	t.Fatalf(
		"Registry must use agentLifetime transitions, not read its private state:\n%s",
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
			"registry.go",
			"registry_lifecycle.go",
		},
		filepath.Join(repositoryPath, "agentloop") + ":agentloop": {
			"agent_host.go",
			"construction.go",
		},
		filepath.Join(repositoryPath, "subagent", "spawn") + ":spawn": {
			"builder.go",
		},
		filepath.Join(repositoryPath, "subagent", "fork") + ":fork": {
			"builder.go",
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

func TestSubagentModesDoNotDependOnSubagentPluginWiring(t *testing.T) {
	t.Parallel()
	repositoryPath := repositoryRoot(t)
	fileSet := token.NewFileSet()
	sourcesByPackage := parsePackages(t, fileSet, repositoryPath)
	packageKeys := []string{
		filepath.Join(
			repositoryPath,
			"subagent",
			"internal",
			"oneshot",
		) + ":oneshot",
		filepath.Join(
			repositoryPath,
			"subagent",
			"internal",
			"continuable",
		) + ":continuable",
		filepath.Join(
			repositoryPath,
			"subagent",
			"internal",
			"bound",
		) + ":bound",
	}
	forbiddenImports := map[string]struct{}{
		"github.com/gorenx/goren/subagent/internal/extension": {},
		"github.com/gorenx/goren/subagent/plugin":             {},
	}
	findings := make([]string, 0)
	for _, packageKey := range packageKeys {
		for _, source := range productionSources(sourcesByPackage[packageKey]) {
			for _, imported := range source.tree.Imports {
				importPath, err := strconv.Unquote(imported.Path.Value)
				if err != nil {
					t.Fatal(err)
				}
				if _, forbidden := forbiddenImports[importPath]; forbidden {
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
		"Subagent modes must own composition without depending on Subagent Plugin wiring:\n%s",
		strings.Join(findings, "\n"),
	)
}

func TestSubagentModuleUsesOnePluginWithoutChildPlugins(t *testing.T) {
	t.Parallel()
	repositoryPath := repositoryRoot(t)
	fileSet := token.NewFileSet()
	sourcesByPackage := parsePackages(t, fileSet, repositoryPath)
	packageKey := filepath.Join(repositoryPath, "subagent", "plugin") + ":plugin"
	receivers := make(map[string]struct{})
	findings := make([]string, 0)
	for _, source := range productionSources(sourcesByPackage[packageKey]) {
		for _, declaration := range source.tree.Decls {
			method, ok := declaration.(*ast.FuncDecl)
			if ok && method.Recv != nil && len(method.Recv.List) == 1 &&
				method.Name.Name == "Manifest" {
				receivers[receiverTypeName(method.Recv.List[0].Type)] = struct{}{}
			}
		}
		ast.Inspect(source.tree, func(node ast.Node) bool {
			assignment, ok := node.(*ast.KeyValueExpr)
			if !ok {
				return true
			}
			field, named := assignment.Key.(*ast.Ident)
			if named && field.Name == "Children" {
				findings = append(
					findings,
					fileSet.Position(field.Pos()).String()+": child Plugin topology",
				)
			}
			return true
		})
	}
	if len(receivers) != 1 {
		findings = append(
			findings,
			"Plugin implementation count = "+strconv.Itoa(len(receivers))+", want 1",
		)
	}
	if _, found := receivers["Plugin"]; !found {
		findings = append(findings, "sole Plugin implementation is not Plugin")
	}
	if len(findings) == 0 {
		return
	}
	sort.Strings(findings)
	t.Fatalf(
		"Subagent module must use one Plugin without child Plugins:\n%s",
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
