package architecture

import (
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestCredentialsBusinessHasNoFrameworkDependency(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join(repositoryRoot(t), "credentials", "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range paths {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		syntax, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imported := range syntax.Imports {
			dependency, err := strconv.Unquote(imported.Path.Value)
			if err != nil {
				t.Fatal(err)
			}
			for _, forbidden := range []string{
				"github.com/tochemey/goakt/",
				"github.com/gorenx/goren/plugin",
				"github.com/gorenx/goren/credentials/",
			} {
				if strings.HasPrefix(dependency, forbidden) {
					t.Errorf("business file %s imports adapter or framework %s", filepath.Base(path), dependency)
				}
			}
		}
	}
}
