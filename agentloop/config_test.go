package agentloop

import (
	"strings"
	"testing"

	"github.com/gorenx/goren/agent"
)

func TestValidateAgentOptionsSubagentDepth(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		depth     int64
		wantError bool
	}{
		{
			name:      "zero",
			depth:     0,
			wantError: false,
		},
		{
			name:      "safe maximum",
			depth:     maxSafeInteger,
			wantError: false,
		},
		{
			name:      "negative",
			depth:     -1,
			wantError: true,
		},
		{
			name:      "above safe maximum",
			depth:     maxSafeInteger + 1,
			wantError: true,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			validationErr := validateAgentOptions(agent.Options{
				SubagentDepth: &testCase.depth,
			})
			if testCase.wantError {
				if validationErr == nil ||
					!strings.Contains(validationErr.Error(), "subagentDepth") {
					t.Fatalf("validation error = %v", validationErr)
				}
				return
			}
			if validationErr != nil {
				t.Fatal(validationErr)
			}
		})
	}
}

func TestCloneAgentOptionsDetachesSubagentDepth(t *testing.T) {
	t.Parallel()
	depthValue := int64(3)
	detached := cloneAgentOptions(agent.Options{
		SubagentDepth: &depthValue,
	})
	depthValue = 8
	if detached.SubagentDepth == nil || *detached.SubagentDepth != 3 {
		t.Fatalf("detached subagentDepth = %v, want 3", detached.SubagentDepth)
	}
}
