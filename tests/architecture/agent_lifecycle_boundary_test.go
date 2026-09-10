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
