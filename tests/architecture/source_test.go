package architecture

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

type goSource struct {
	path string
	tree *ast.File
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve architecture source path")
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
		sourcesByPackage[packageKey] = append(sourcesByPackage[packageKey], goSource{
			path: path,
			tree: tree,
		})
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
