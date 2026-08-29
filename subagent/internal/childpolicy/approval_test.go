package childpolicy

import (
	"context"
	"testing"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/session"
)

func TestDelegationSeedUsesUnpublishedScopeAgent(t *testing.T) {
	t.Parallel()
	conversation, err := session.New(
		"delegated-child",
		session.CreateOptions{},
	)
	if err != nil {
		t.Fatal(err)
	}
	record := &delegationRecord{}
	scope := &delegationScope{
		subject: &delegationAgent{
			conversation: conversation,
		},
	}
	if err = agent.ApplyProvisioning(
		context.Background(),
		scope,
		DelegationSeed(record),
	); err != nil {
		t.Fatal(err)
	}
	if record.conversation != conversation {
		t.Fatal("delegation policy did not receive the Scope Agent Session")
	}
	if scope.owned != 0 {
		t.Fatalf("owned Scope resources = %d, want 0", scope.owned)
	}
}

type delegationScope struct {
	subject agent.Agent
	owned   int
}

func (scope *delegationScope) Agent() agent.Agent {
	return scope.subject
}

func (scope *delegationScope) Own(agent.ScopeResource) error {
	scope.owned++
	return nil
}

type delegationAgent struct {
	conversation session.Context
}

func (*delegationAgent) ID() session.SessionID {
	return "delegated-child"
}

func (*delegationAgent) OptionsValue() agent.Options {
	return agent.Options{}
}

func (subject *delegationAgent) SessionValue() session.Context {
	return subject.conversation
}

func (*delegationAgent) InboxValue() *agent.Inbox {
	return nil
}

func (*delegationAgent) StatusValue() agent.Status {
	return agent.StatusIdle
}

func (*delegationAgent) Cancel(agent.CancelCause, agent.CancelOptions) {}

func (*delegationAgent) WhenIdle(context.Context) error {
	return nil
}

func (*delegationAgent) RunMaintenance(
	requestContext context.Context,
	operation func(context.Context) error,
) error {
	return operation(requestContext)
}

func (*delegationAgent) Followup(agentmessage.UserMessage) error {
	return nil
}

func (*delegationAgent) Steer(agentmessage.UserMessage) error {
	return nil
}

func (*delegationAgent) Inject(agentmessage.UserMessage) error {
	return nil
}

type delegationRecord struct {
	plugin.Base
	conversation session.Context
}

func (*delegationRecord) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: "test/delegation-policy",
	}
}

func (*delegationRecord) Apply(context.Context) error   { return nil }
func (*delegationRecord) Dispose(context.Context) error { return nil }
func (record *delegationRecord) SeedDelegationPolicy(
	_ context.Context,
	conversation session.Context,
) error {
	record.conversation = conversation
	return nil
}
