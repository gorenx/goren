package architecture

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"unicode"
)

func TestSubagentMethodFilesIdentifyOneReceiver(t *testing.T) {
	t.Parallel()
	repositoryDirectory := repositoryRoot(t)
	targetDirectories := []string{
		filepath.Join(repositoryDirectory, "subagent", "internal", "bound"),
		filepath.Join(repositoryDirectory, "subagent", "internal", "continuable"),
	}
	findings := []string{}
	for _, targetDirectory := range targetDirectories {
		directoryEntries, err := os.ReadDir(targetDirectory)
		if err != nil {
			t.Fatal(err)
		}
		for _, directoryEntry := range directoryEntries {
			if directoryEntry.IsDir() ||
				!strings.HasSuffix(directoryEntry.Name(), ".go") ||
				strings.HasSuffix(directoryEntry.Name(), "_test.go") {
				continue
			}
			filename := filepath.Join(targetDirectory, directoryEntry.Name())
			fileSet := token.NewFileSet()
			tree, parseErr := parser.ParseFile(fileSet, filename, nil, 0)
			if parseErr != nil {
				t.Fatal(parseErr)
			}
			methodOwners := map[string]struct{}{}
			for _, declaration := range tree.Decls {
				method, matches := declaration.(*ast.FuncDecl)
				if !matches || method.Recv == nil || len(method.Recv.List) == 0 {
					continue
				}
				methodOwner, found := methodReceiverName(method.Recv.List[0].Type)
				if !found {
					position := fileSet.Position(method.Pos())
					findings = append(findings, fmt.Sprintf(
						"%s:%d: unsupported method receiver",
						position.Filename,
						position.Line,
					))
					continue
				}
				methodOwners[methodOwner] = struct{}{}
			}
			if len(methodOwners) == 0 {
				continue
			}
			owners := make([]string, 0, len(methodOwners))
			for methodOwner := range methodOwners {
				owners = append(owners, methodOwner)
			}
			sort.Strings(owners)
			if len(owners) != 1 {
				findings = append(findings, fmt.Sprintf(
					"%s: mixes method receivers %s",
					filename,
					strings.Join(owners, ", "),
				))
				continue
			}
			fileBase := strings.TrimSuffix(directoryEntry.Name(), ".go")
			receiverPrefix := snakeFilename(owners[0])
			if fileBase != receiverPrefix &&
				!strings.HasPrefix(fileBase, receiverPrefix+"_") {
				findings = append(findings, fmt.Sprintf(
					"%s: methods on %s require filename prefix %s",
					filename,
					owners[0],
					receiverPrefix,
				))
			}
		}
	}
	if len(findings) == 0 {
		return
	}
	sort.Strings(findings)
	t.Fatalf(
		"Subagent method files must contain one receiver and use its filename prefix:\n%s",
		strings.Join(findings, "\n"),
	)
}

func methodReceiverName(receiver ast.Expr) (string, bool) {
	switch receiverValue := receiver.(type) {
	case *ast.Ident:
		return receiverValue.Name, true
	case *ast.StarExpr:
		identifier, matches := receiverValue.X.(*ast.Ident)
		if !matches {
			return "", false
		}
		return identifier.Name, true
	default:
		return "", false
	}
}

func snakeFilename(name string) string {
	runes := []rune(name)
	var result strings.Builder
	for index, currentRune := range runes {
		if unicode.IsUpper(currentRune) {
			if index > 0 &&
				(unicode.IsLower(runes[index-1]) ||
					(index+1 < len(runes) && unicode.IsLower(runes[index+1]))) {
				result.WriteByte('_')
			}
			result.WriteRune(unicode.ToLower(currentRune))
			continue
		}
		result.WriteRune(currentRune)
	}
	return result.String()
}
