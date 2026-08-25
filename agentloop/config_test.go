package agentloop

import (
	"strings"
	"testing"

	"github.com/gorenx/goren/agent"
)

func TestValidateAgentOptionsMaxTokens(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		maxTokens int
		wantError bool
	}{
		{
			name:      "positive",
			maxTokens: 1,
		},
		{
			name:      "zero",
			maxTokens: 0,
			wantError: true,
		},
		{
			name:      "negative",
			maxTokens: -1,
			wantError: true,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			validationErr := validateAgentOptions(agent.Options{
				MaxTokens: &testCase.maxTokens,
			})
			if testCase.wantError {
				if validationErr == nil ||
					!strings.Contains(validationErr.Error(), "maxTokens") {
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

func TestCloneAgentOptionsDetachesMaxTokens(t *testing.T) {
	t.Parallel()
	maxTokens := 3
	detached := cloneAgentOptions(agent.Options{
		MaxTokens: &maxTokens,
	})
	maxTokens = 8
	if detached.MaxTokens == nil || *detached.MaxTokens != 3 {
		t.Fatalf("detached maxTokens = %v, want 3", detached.MaxTokens)
	}
}
