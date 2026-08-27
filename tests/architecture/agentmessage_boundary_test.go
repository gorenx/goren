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

var agentMessageFamily = map[string]struct{}{
	"AssistantMessage":         {},
	"AssistantMessageInput":    {},
	"BoundContextSummary":      {},
	"CallID":                   {},
	"CloneAssistantMessage":    {},
	"CloneContentBlocks":       {},
	"CloneMessages":            {},
	"CloneToolResultMessage":   {},
	"CloneUserMessage":         {},
	"ContentBlock":             {},
	"ContentHasImage":          {},
	"ContextForm":              {},
	"ContextSnapshotSection":   {},
	"DecodeContentBlock":       {},
	"DecodeContentBlocks":      {},
	"DecodeMessage":            {},
	"DecodeUserMessage":        {},
	"ImageBlock":               {},
	"InvalidContentBlockError": {},
	"Message":                  {},
	"MessageID":                {},
	"MessageInput":             {},
	"MessageRole":              {},
	"MessageSource":            {},
	"ModelMessageSource":       {},
	"NewAssistantMessage":      {},
	"NewMessage":               {},
	"NewOpaqueContentBlock":    {},
	"NewOpaqueMessageSource":   {},
	"NewTextBlock":             {},
	"NewToolResultMessage":     {},
	"NewUserMessage":           {},
	"OpaqueContentBlock":       {},
	"OpaqueMessageSource":      {},
	"PlainTextContent":         {},
	"PluginMessageSource":      {},
	"ReasoningBlock":           {},
	"ReplaceSource":            {},
	"RoleAssistant":            {},
	"RoleSystem":               {},
	"RoleUser":                 {},
	"TextBlock":                {},
	"ToolCallBlock":            {},
	"ToolMessageSource":        {},
	"ToolResultBlock":          {},
	"ToolResultMessage":        {},
	"ToolResultMessageInput":   {},
	"UserMessage":              {},
	"UserMessageInput":         {},
	"UserMessageSource":        {},
	"ContextCatalog":           {},
	"ContextInstructions":      {},
	"ContextNotice":            {},
	"ContextRecall":            {},
	"ContextRelay":             {},
	"ContextSnapshot":          {},
	"ContextSummaryMaxChars":   {},
}

func TestAgentMessageDoesNotImportConsumersOrRuntimeAdapters(t *testing.T) {
	t.Parallel()
	repositoryPath := repositoryRoot(t)
	fileSet := token.NewFileSet()
	sourcesByPackage := parsePackages(t, fileSet, repositoryPath)
	sources := productionSources(sourcesByPackage[filepath.Join(repositoryPath, "agentmessage")+":agentmessage"])
	if len(sources) == 0 {
		t.Fatal("agentmessage package sources not found")
	}
	forbiddenPrefixes := []string{
		"github.com/gorenx/goren/agent",
		"github.com/gorenx/goren/apiproxy",
		"github.com/gorenx/goren/connection",
		"github.com/gorenx/goren/llm",
		"github.com/gorenx/goren/plugin",
		"github.com/gorenx/goren/session",
		"github.com/gorenx/goren/subagent",
		"github.com/gorenx/goren/tools",
	}
	findings := make([]string, 0)
	for _, source := range sources {
		for _, imported := range source.tree.Imports {
			importPath, err := strconv.Unquote(imported.Path.Value)
			if err != nil {
				t.Fatal(err)
			}
			for _, forbidden := range forbiddenPrefixes {
				if importPath == forbidden || strings.HasPrefix(importPath, forbidden+"/") {
					findings = append(findings, fileSet.Position(imported.Pos()).String()+": "+importPath)
				}
			}
		}
	}
	if len(findings) != 0 {
		sort.Strings(findings)
		t.Fatalf("agentmessage must not import consumers or runtime adapters:\n%s", strings.Join(findings, "\n"))
	}
}

func TestLLMDoesNotRedeclareAgentMessageFamily(t *testing.T) {
	t.Parallel()
	repositoryPath := repositoryRoot(t)
	fileSet := token.NewFileSet()
	sourcesByPackage := parsePackages(t, fileSet, repositoryPath)
	sources := productionSources(sourcesByPackage[filepath.Join(repositoryPath, "llm")+":llm"])
	if len(sources) == 0 {
		t.Fatal("llm package sources not found")
	}
	findings := make([]string, 0)
	for _, source := range sources {
		for _, declaration := range source.tree.Decls {
			switch current := declaration.(type) {
			case *ast.FuncDecl:
				if _, forbidden := agentMessageFamily[current.Name.Name]; forbidden {
					findings = append(findings, fileSet.Position(current.Name.Pos()).String())
				}
			case *ast.GenDecl:
				for _, specification := range current.Specs {
					switch typedSpecification := specification.(type) {
					case *ast.TypeSpec:
						if _, forbidden := agentMessageFamily[typedSpecification.Name.Name]; forbidden {
							findings = append(findings, fileSet.Position(typedSpecification.Name.Pos()).String())
						}
					case *ast.ValueSpec:
						for _, name := range typedSpecification.Names {
							if _, forbidden := agentMessageFamily[name.Name]; forbidden {
								findings = append(findings, fileSet.Position(name.Pos()).String())
							}
						}
					}
				}
			}
		}
	}
	if len(findings) != 0 {
		sort.Strings(findings)
		t.Fatalf("llm must not redeclare or forward Agent Message contracts:\n%s", strings.Join(findings, "\n"))
	}
}

func TestProductionCodeDoesNotUseRemovedLLMMessageFamily(t *testing.T) {
	t.Parallel()
	repositoryPath := repositoryRoot(t)
	fileSet := token.NewFileSet()
	sourcesByPackage := parsePackages(t, fileSet, repositoryPath)
	findings := make([]string, 0)
	for _, packageSources := range sourcesByPackage {
		for _, source := range productionSources(packageSources) {
			ast.Inspect(source.tree, func(node ast.Node) bool {
				selector, ok := node.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				packageName, selectedPackage := selectorPackage(selector)
				if !selectedPackage || packageName != "llm" {
					return true
				}
				if _, forbidden := agentMessageFamily[selector.Sel.Name]; forbidden {
					findings = append(findings, fileSet.Position(selector.Pos()).String())
				}
				return true
			})
		}
	}
	if len(findings) != 0 {
		sort.Strings(findings)
		t.Fatalf("production code uses removed llm Agent Message contracts:\n%s", strings.Join(findings, "\n"))
	}
}
