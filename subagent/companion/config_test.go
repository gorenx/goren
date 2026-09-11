package companion_test

import (
	"testing"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/mcpclient"
	"github.com/gorenx/goren/subagent/companion"
	"github.com/gorenx/goren/systemprompt"
	"github.com/gorenx/goren/tools"
)

func TestConfigurationSnapshotDoesNotShareMutableValues(t *testing.T) {
	limit := 100
	flag := false
	input := companion.Config{
		Model: agent.Options{
			MaxTokens: &limit,
		},
		Prompt: systemprompt.Config{
			IncludeRuntimeContext: &flag,
			ToolOrder:             []string{"<unlisted-tools>"},
		},
		Tools: map[string]tools.Config{
			"read": {
				Enabled: &flag,
			},
		},
		MCP: []mcpclient.Config{{
			ServerName: "local",
			Transport:  "stdio",
			Command:    "fixture",
			Args:       []string{"original"},
		}},
	}
	snapshot := input.Clone()
	limit = 200
	flag = true
	input.Prompt.ToolOrder[0] = "changed"
	input.MCP[0].Args[0] = "changed"
	delete(input.Tools, "read")
	if *snapshot.Model.MaxTokens != 100 || *snapshot.Prompt.IncludeRuntimeContext || snapshot.Prompt.ToolOrder[0] != "<unlisted-tools>" || *snapshot.Tools["read"].Enabled || snapshot.MCP[0].Args[0] != "original" {
		t.Fatal("inherited configuration shares mutable values")
	}
	if err := snapshot.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestConfigurationRejectsInvalidInheritanceAndReservedTool(t *testing.T) {
	for _, input := range []companion.Config{
		{
			Inherit: companion.Inheritance{
				Tools: "selected",
			},
		},
		{
			Tools: map[string]tools.Config{
				"report": {},
			},
		},
	} {
		if input.Validate() == nil {
			t.Fatal("invalid configuration accepted")
		}
	}
	for _, selection := range []companion.ToolInheritance{"", companion.InheritNoTools, companion.InheritAllTools} {
		if err := (companion.Config{
			Inherit: companion.Inheritance{
				Tools: selection,
			},
		}).Validate(); err != nil {
			t.Fatal(err)
		}
	}
}
