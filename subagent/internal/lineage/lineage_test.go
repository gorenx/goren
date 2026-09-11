package lineage

import (
	"strings"
	"testing"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/session"
)

func TestFromInheritsParentAndResolvesChildFacts(t *testing.T) {
	maxDepth := int64(4)
	headerDepth := int64(3)
	cwd := "/workspace"
	preset := "coding"
	childLineage, err := From(
		"parent",
		session.Header{
			CWD:             &cwd,
			DelegationDepth: &headerDepth,
			AgentPreset:     &preset,
		},
		agent.Options{
			Provider: "deepseek",
			Model:    "chat",
		},
		&maxDepth,
	)
	if err != nil {
		t.Fatal(err)
	}
	maxTokens := 4096
	resolved := childLineage.AgentOptions(&agent.Options{
		Model:     "reasoner",
		MaxTokens: &maxTokens,
	})
	if resolved.Provider != "deepseek" || resolved.Model != "reasoner" ||
		resolved.MaxTokens == nil || *resolved.MaxTokens != 4096 {
		t.Fatalf("resolved options = %#v", resolved)
	}
	metadata := childLineage.Metadata(5)
	if metadata.ParentSession == nil || *metadata.ParentSession != "parent" ||
		metadata.DelegationDepth == nil || *metadata.DelegationDepth != 4 ||
		metadata.SeedLength == nil || *metadata.SeedLength != 5 ||
		metadata.CWD == nil || *metadata.CWD != cwd ||
		metadata.AgentPreset == nil || *metadata.AgentPreset != preset ||
		metadata.Origin != session.OriginSubagent {
		t.Fatalf("metadata = %#v", metadata)
	}
}

func TestFromRejectsInvalidDepth(t *testing.T) {
	negative := int64(-1)
	unsafe := maxSafeInteger + 1
	zero := int64(0)
	testCases := []struct {
		name     string
		depth    int64
		maximum  *int64
		contains string
	}{
		{
			name:     "negative cap",
			maximum:  &negative,
			contains: "non-negative safe integer",
		},
		{
			name:     "unsafe cap",
			maximum:  &unsafe,
			contains: "non-negative safe integer",
		},
		{
			name:     "child exceeds cap",
			maximum:  &zero,
			contains: "exceeds maxDepth",
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := From(
				"parent",
				session.Header{
					DelegationDepth: &testCase.depth,
				},
				agent.Options{},
				testCase.maximum,
			)
			if err == nil || !strings.Contains(err.Error(), testCase.contains) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}
