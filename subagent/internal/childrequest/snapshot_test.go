package childrequest

import (
	"testing"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/subagent"
	"github.com/gorenx/goren/tools"
)

func TestSnapshotDetachesSharedChildInputs(t *testing.T) {
	t.Parallel()
	maxTokens := 128
	maxDepth := int64(3)
	persona := "reviewer"
	source := subagent.ChildRequest{
		Prompt: []llm.ContentBlock{
			llm.NewTextBlock("inspect"),
		},
		AgentOptions: &agent.Options{
			Provider:  "provider",
			Model:     "model",
			MaxTokens: &maxTokens,
		},
		MaxDepth: &maxDepth,
		ToolFilter: &tools.ToolRestriction{
			Allow: []string{"read"},
			Deny:  []string{"write"},
		},
		Persona: &persona,
	}
	detached, snapshotErr := Snapshot(source)
	if snapshotErr != nil {
		t.Fatal(snapshotErr)
	}
	maxTokens = 1
	maxDepth = 1
	persona = "changed"
	source.ToolFilter.Allow[0] = "changed"
	source.ToolFilter.Deny[0] = "changed"
	if *detached.AgentOptions.MaxTokens != 128 || *detached.MaxDepth != 3 ||
		*detached.Persona != "reviewer" || detached.ToolFilter.Allow[0] != "read" ||
		detached.ToolFilter.Deny[0] != "write" {
		t.Fatalf("snapshot changed with caller-owned input: %#v", detached)
	}
}

func TestSnapshotRejectsInvalidSharedChildInputs(t *testing.T) {
	t.Parallel()
	negativeDepth := int64(-1)
	cases := []struct {
		name    string
		request subagent.ChildRequest
	}{
		{
			name: "negative max depth",
			request: subagent.ChildRequest{
				MaxDepth: &negativeDepth,
			},
		},
		{
			name: "empty tool restriction",
			request: subagent.ChildRequest{
				ToolFilter: &tools.ToolRestriction{},
			},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			if _, snapshotErr := Snapshot(testCase.request); snapshotErr == nil {
				t.Fatal("invalid child request was accepted")
			}
		})
	}
}
