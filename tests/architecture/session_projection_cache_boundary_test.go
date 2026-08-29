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

func TestSessionProjectionCacheDependencyDirection(t *testing.T) {
	t.Parallel()
	repositoryPath := repositoryRoot(t)
	fileSet := token.NewFileSet()
	sourcesByPackage := parsePackages(t, fileSet, repositoryPath)
	cacheImport := "github.com/gorenx/goren/session/projectioncache"
	testCases := []struct {
		packagePath      string
		packageName      string
		forbiddenImports []string
	}{
		{
			packagePath: filepath.Join(repositoryPath, "agent"),
			packageName: "agent",
			forbiddenImports: []string{
				cacheImport,
			},
		},
		{
			packagePath: filepath.Join(repositoryPath, "agentloop"),
			packageName: "agentloop",
			forbiddenImports: []string{
				cacheImport,
			},
		},
		{
			packagePath: filepath.Join(repositoryPath, "session"),
			packageName: "session",
			forbiddenImports: []string{
				cacheImport,
			},
		},
		{
			packagePath: filepath.Join(repositoryPath, "session", "projection"),
			packageName: "projection",
			forbiddenImports: []string{
				cacheImport,
			},
		},
		{
			packagePath: filepath.Join(repositoryPath, "session", "projectioncache"),
			packageName: "projectioncache",
			forbiddenImports: []string{
				cacheImport + "/factory",
				cacheImport + "/plugin",
				cacheImport + "/sqlite",
				"github.com/gorenx/goren/apiproxy",
				"github.com/gorenx/goren/internal/assembly",
				"github.com/gorenx/goren/subagent",
			},
		},
		{
			packagePath: filepath.Join(repositoryPath, "apiproxy", "session"),
			packageName: "sessionapi",
			forbiddenImports: []string{
				cacheImport,
				cacheImport + "/plugin",
				cacheImport + "/sqlite",
			},
		},
		{
			packagePath: filepath.Join(repositoryPath, "subagent", "internal", "childdirectory"),
			packageName: "childdirectory",
			forbiddenImports: []string{
				cacheImport,
				cacheImport + "/plugin",
				cacheImport + "/sqlite",
			},
		},
	}
	findings := []string{}
	for _, testCase := range testCases {
		packageKey := testCase.packagePath + ":" + testCase.packageName
		for _, source := range sourcesByPackage[packageKey] {
			for _, imported := range source.tree.Imports {
				importPath, err := strconv.Unquote(imported.Path.Value)
				if err != nil {
					t.Fatal(err)
				}
				for _, forbidden := range testCase.forbiddenImports {
					if importPath == forbidden || strings.HasPrefix(importPath, forbidden+"/") {
						findings = append(
							findings,
							fileSet.Position(imported.Pos()).String()+": "+importPath,
						)
					}
				}
			}
		}
	}
	if len(findings) != 0 {
		sort.Strings(findings)
		t.Fatalf(
			"Session Projection Cache dependency direction was bypassed:\n%s",
			strings.Join(findings, "\n"),
		)
	}
}

func TestProjectionCachePluginAndSQLiteStaySeparate(t *testing.T) {
	t.Parallel()
	repositoryPath := repositoryRoot(t)
	fileSet := token.NewFileSet()
	sourcesByPackage := parsePackages(t, fileSet, repositoryPath)
	testCases := []struct {
		packageKey string
		forbidden  string
	}{
		{
			packageKey: filepath.Join(
				repositoryPath,
				"session",
				"projectioncache",
				"plugin",
			) + ":plugin",
			forbidden: "github.com/gorenx/goren/session/projectioncache/sqlite",
		},
		{
			packageKey: filepath.Join(
				repositoryPath,
				"session",
				"projectioncache",
				"sqlite",
			) + ":sqlite",
			forbidden: "github.com/gorenx/goren/plugin",
		},
	}
	findings := []string{}
	for _, testCase := range testCases {
		for _, source := range sourcesByPackage[testCase.packageKey] {
			ast.Inspect(source.tree, func(node ast.Node) bool {
				imported, ok := node.(*ast.ImportSpec)
				if !ok {
					return true
				}
				importPath, err := strconv.Unquote(imported.Path.Value)
				if err == nil && importPath == testCase.forbidden {
					findings = append(
						findings,
						fileSet.Position(imported.Pos()).String()+": "+importPath,
					)
				}
				return true
			})
		}
	}
	if len(findings) != 0 {
		sort.Strings(findings)
		t.Fatalf(
			"Projection Cache Plugin and SQLite responsibilities are mixed:\n%s",
			strings.Join(findings, "\n"),
		)
	}
}

func TestRemovedProjectionCacheLifecycleTypesStayAbsent(t *testing.T) {
	t.Parallel()
	repositoryPath := repositoryRoot(t)
	fileSet := token.NewFileSet()
	sourcesByPackage := parsePackages(t, fileSet, repositoryPath)
	cacheSources := sourcesByPackage[filepath.Join(repositoryPath, "session", "projectioncache")+":projectioncache"]
	removedTypes := map[string]struct{}{
		"Coordinator":        {},
		"checkpointSchedule": {},
		"checkpointWriter":   {},
		"recordCache":        {},
		"sessionCache":       {},
	}
	removedMethods := map[string]struct{}{
		"Advance": {},
		"Begin":   {},
		"Retire":  {},
	}
	findings := []string{}
	for _, source := range cacheSources {
		for _, declaration := range source.tree.Decls {
			switch current := declaration.(type) {
			case *ast.FuncDecl:
				if _, removed := removedMethods[current.Name.Name]; removed {
					findings = append(findings, fileSet.Position(current.Name.Pos()).String())
				}
			case *ast.GenDecl:
				for _, specification := range current.Specs {
					typeSpecification, ok := specification.(*ast.TypeSpec)
					if !ok {
						continue
					}
					if _, removed := removedTypes[typeSpecification.Name.Name]; removed {
						findings = append(
							findings,
							fileSet.Position(typeSpecification.Name.Pos()).String(),
						)
					}
				}
			}
		}
	}
	if len(findings) != 0 {
		sort.Strings(findings)
		t.Fatalf(
			"removed Projection Cache lifecycle APIs were reintroduced:\n%s",
			strings.Join(findings, "\n"),
		)
	}
}
