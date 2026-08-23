package lineage

import (
	"context"
	"strings"
	"testing"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/session"
)

type parentRecord struct {
	plugin.Base
	conversation *session.Session
	options      agent.Options
}

func (*parentRecord) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: "lineage-test-parent",
	}
}

func (*parentRecord) Apply(context.Context) error { return nil }

func (*parentRecord) Dispose(context.Context) error { return nil }

func (subject *parentRecord) ID() session.SessionID { return subject.conversation.ID() }

func (subject *parentRecord) OptionsValue() agent.Options { return subject.options }

func (subject *parentRecord) SessionValue() *session.Session { return subject.conversation }

func (*parentRecord) InboxValue() *agent.Inbox { return nil }

func (*parentRecord) StatusValue() agent.Status { return agent.StatusIdle }

func (*parentRecord) Cancel(agent.CancelCause, agent.CancelOptions) {}

func (*parentRecord) WhenIdle(context.Context) error { return nil }

func (*parentRecord) RunMaintenance(
	requestContext context.Context,
	task agent.MaintenanceTask,
) error {
	return task.Run(requestContext)
}

func (*parentRecord) Send(llm.UserMessage, agent.InboxTarget, bool) error { return nil }

func (*parentRecord) Followup(llm.UserMessage) error { return nil }

func (*parentRecord) Steer(llm.UserMessage) error { return nil }

func (*parentRecord) Inject(llm.UserMessage) error { return nil }

func TestFromInheritsParentAndResolvesChildFacts(t *testing.T) {
	parentAgent := newParent(t, 2, 3)
	maxDepth := int64(4)

	childLineage, lineageErr := From(parentAgent, &maxDepth)
	if lineageErr != nil {
		t.Fatalf("From returned error: %v", lineageErr)
	}
	maxTokens := 4096
	resolved := childLineage.AgentOptions(
		&agent.Options{
			Model:     "reasoner",
			MaxTokens: &maxTokens,
		},
	)
	if resolved.Provider != "deepseek" || resolved.Model != "reasoner" {
		t.Fatalf("unexpected inherited options: %#v", resolved)
	}
	if resolved.MaxTokens == nil || *resolved.MaxTokens != 4096 {
		t.Fatalf("unexpected max tokens: %#v", resolved.MaxTokens)
	}
	if resolved.SubagentDepth == nil || *resolved.SubagentDepth != 4 {
		t.Fatalf("unexpected child depth: %#v", resolved.SubagentDepth)
	}

	sessionMetadata := childLineage.Metadata(5)
	if sessionMetadata.ParentSession == nil || *sessionMetadata.ParentSession != parentAgent.ID() {
		t.Fatalf("unexpected parent Session: %#v", sessionMetadata.ParentSession)
	}
	if sessionMetadata.DelegationDepth == nil || *sessionMetadata.DelegationDepth != 4 {
		t.Fatalf("unexpected metadata depth: %#v", sessionMetadata.DelegationDepth)
	}
	if sessionMetadata.SeedLength == nil || *sessionMetadata.SeedLength != 5 {
		t.Fatalf("unexpected seed length: %#v", sessionMetadata.SeedLength)
	}
	if sessionMetadata.CWD == nil || *sessionMetadata.CWD != "/workspace" {
		t.Fatalf("unexpected cwd: %#v", sessionMetadata.CWD)
	}
	if sessionMetadata.AgentPreset == nil || *sessionMetadata.AgentPreset != "coding" {
		t.Fatalf("unexpected preset: %#v", sessionMetadata.AgentPreset)
	}
	if sessionMetadata.Origin != session.OriginSubagent {
		t.Fatalf("unexpected origin: %q", sessionMetadata.Origin)
	}
}

func TestFromRejectsInvalidDepth(t *testing.T) {
	parentAgent := newParent(t, 0, 0)
	negative := int64(-1)
	unsafe := maxSafeInteger + 1
	zero := int64(0)

	tests := []struct {
		name     string
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
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			_, lineageErr := From(parentAgent, testCase.maximum)
			if lineageErr == nil || !contains(lineageErr.Error(), testCase.contains) {
				t.Fatalf("unexpected error: %v", lineageErr)
			}
		})
	}
}

func newParent(t *testing.T, optionDepth int64, headerDepth int64) agent.Agent {
	t.Helper()
	parentSession, sessionErr := session.New(
		"parent",
		session.CreateOptions{
			Metadata: session.Metadata{
				CWD:             stringPointerValue("/workspace"),
				DelegationDepth: int64Pointer(headerDepth),
				AgentPreset:     stringPointerValue("coding"),
			},
		},
	)
	if sessionErr != nil {
		t.Fatalf("session.New returned error: %v", sessionErr)
	}
	return &parentRecord{
		conversation: parentSession,
		options: agent.Options{
			Provider:      "deepseek",
			Model:         "chat",
			SubagentDepth: int64Pointer(optionDepth),
		},
	}
}

func contains(value string, fragment string) bool {
	return strings.Contains(value, fragment)
}

func stringPointerValue(value string) *string {
	return &value
}
