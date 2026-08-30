package childpolicy

import (
	"context"
	"testing"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/systemprompt"
	"github.com/gorenx/goren/tools"
)

type setupEditorRecord struct {
	agent.ScopeEditor
	sections     []systemprompt.PromptSection
	restrictions []string
}

func (record *setupEditorRecord) AddPromptSection(
	_ context.Context,
	section systemprompt.PromptSection,
) error {
	record.sections = append(record.sections, section)
	return nil
}
func (record *setupEditorRecord) AddToolRestriction(
	_ context.Context,
	name string,
	_ tools.ToolRestriction,
) error {
	record.restrictions = append(record.restrictions, name)
	return nil
}

func TestPolicySetupAddsSelectedContributions(t *testing.T) {
	t.Parallel()
	persona := "review carefully"
	restriction := tools.ToolRestriction{
		Allow: []string{"read"},
	}
	configured := Setup(PolicySet{
		Persona:         &persona,
		ToolRestriction: &restriction,
	})
	if configured == nil {
		t.Fatal("Policy Setup is nil")
	}
	editor := &setupEditorRecord{}
	if err := configured.Apply(context.Background(), nil, editor); err != nil {
		t.Fatal(err)
	}
	if len(editor.sections) != 1 ||
		editor.sections[0].Name != systemprompt.PersonaSection {
		t.Fatalf("sections = %#v", editor.sections)
	}
	if len(editor.restrictions) != 1 || editor.restrictions[0] != restrictionName {
		t.Fatalf("restrictions = %v", editor.restrictions)
	}
	if Setup(PolicySet{}) != nil {
		t.Fatal("empty policy returned a Setup")
	}
}

func TestDelegationSeedUsesUnpublishedAgentSession(t *testing.T) {
	t.Parallel()
	conversation, err := session.New("delegated-child", session.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	record := &delegationRecord{}
	subject := &delegationAgent{
		conversation: conversation,
	}
	if err = DelegationSeed(record).Apply(
		context.Background(),
		subject,
		&setupEditorRecord{},
	); err != nil {
		t.Fatal(err)
	}
	if record.conversation != conversation {
		t.Fatal("delegation policy did not receive child Session")
	}
}

type delegationAgent struct {
	conversation session.Context
}

func (*delegationAgent) ID() session.SessionID                         { return "delegated-child" }
func (*delegationAgent) OptionsValue() agent.Options                   { return agent.Options{} }
func (subject *delegationAgent) SessionValue() session.Context         { return subject.conversation }
func (*delegationAgent) InboxValue() *agent.Inbox                      { return nil }
func (*delegationAgent) StatusValue() agent.Status                     { return agent.StatusIdle }
func (*delegationAgent) Cancel(agent.CancelCause, agent.CancelOptions) {}
func (*delegationAgent) WhenIdle(context.Context) error                { return nil }
func (*delegationAgent) RunMaintenance(
	requestContext context.Context,
	operation func(context.Context) error,
) error {
	return operation(requestContext)
}
func (*delegationAgent) Followup(agentmessage.UserMessage) error { return nil }
func (*delegationAgent) Steer(agentmessage.UserMessage) error    { return nil }
func (*delegationAgent) Inject(agentmessage.UserMessage) error   { return nil }

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

var _ agent.ScopeEditor = (*setupEditorRecord)(nil)
var _ agent.Agent = (*delegationAgent)(nil)
