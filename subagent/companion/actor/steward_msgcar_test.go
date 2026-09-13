package companionactor

import (
	"fmt"
	"testing"
	"time"

	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/session"
	sessionactor "github.com/gorenx/goren/session/actor"
	sessionregistry "github.com/gorenx/goren/session/registry"
)

func TestStewardRunsMsgcarOnlyForMemoryCompanion(t *testing.T) {
	fixture := newStewardship(t)
	if _, err := fixture.client.Create(t.Context(), Create{
		Name:    "reviewer",
		Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	reviewer := awaitPhase(t, fixture.client, "reviewer", Ready)
	if _, err := fixture.system.ActorOf(t.Context(), msgcarName(reviewer)); err == nil {
		t.Fatal("ordinary Companion started Msgcar")
	}
	if _, err := fixture.client.Create(t.Context(), Create{
		Name:    "MeMoRy",
		Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	memory := awaitPhase(t, fixture.client, "MeMoRy", Ready)
	listener, err := fixture.system.ActorOf(t.Context(), msgcarName(memory))
	if err != nil || listener == nil || !listener.IsRunning() {
		t.Fatalf("memory Companion has no Msgcar: %v", err)
	}
	disabled, err := fixture.client.Configure(t.Context(), Configure{
		Name:             "MeMoRy",
		ExpectedRevision: memory.Companion.Revision,
		Enabled:          false,
	})
	if err != nil {
		t.Fatal(err)
	}
	awaitPhase(t, fixture.client, "MeMoRy", Stopped)
	if listener.IsRunning() {
		t.Fatal("disabled memory Companion retained Msgcar")
	}
	if _, err := fixture.client.Configure(t.Context(), Configure{
		Name:             "MeMoRy",
		ExpectedRevision: disabled.Companion.Revision,
		Enabled:          true,
	}); err != nil {
		t.Fatal(err)
	}
	restarted := awaitPhase(t, fixture.client, "MeMoRy", Ready)
	if restarted.Creation.CreationID == memory.Creation.CreationID {
		t.Fatal("memory Companion reused old Creation")
	}
	replacement, err := fixture.system.ActorOf(t.Context(), msgcarName(restarted))
	if err != nil || replacement == nil || !replacement.IsRunning() {
		t.Fatalf("restored memory Companion has no Msgcar: %v", err)
	}
	if err := fixture.client.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if replacement.IsRunning() {
		t.Fatal("parent close retained memory Msgcar")
	}
}

func msgcarName(view View) string {
	return fmt.Sprintf("msgcar.%s.%s", view.Creation.AgentID, view.Creation.CreationID)
}

func TestMemoryMsgcarFollowsRealParentSessionCommit(t *testing.T) {
	fixture := newStewardship(t)
	if _, err := fixture.client.Create(t.Context(), Create{
		Name:    "MEMORY",
		Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	memory := awaitPhase(t, fixture.client, "MEMORY", Ready)
	fixture.hold(t, memory.Creation)
	parent := request[sessionregistry.Endpoint](t, fixture.system, fixture.sessions, sessionregistry.ResolveSession{
		SessionID:  fixture.input.Creation.AgentID,
		CreationID: fixture.input.Creation.CreationID,
	})
	if parent.Err != nil {
		t.Fatal(parent.Err)
	}
	input, err := agentmessage.NewUserMessage(agentmessage.UserMessageInput{
		Content: []agentmessage.ContentBlock{agentmessage.NewTextBlock("remember this fact")},
		Source:  agentmessage.UserMessageSource{},
	})
	if err != nil {
		t.Fatal(err)
	}
	draft, err := session.NewSurfaceEventDraft(session.UserMessageAdded, input, session.SurfaceIntent{
		Operation: session.SurfaceAppend(),
	})
	if err != nil {
		t.Fatal(err)
	}
	committed := request[sessionactor.Committed](t, fixture.system, parent.Target, sessionactor.Commit{
		CreationID: fixture.input.Creation.CreationID,
		Plan:       session.Batch(draft),
	})
	if committed.Err != nil {
		t.Fatal(committed.Err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		current := fixture.activity(t, memory.Creation)
		for _, message := range current.Pending.NextTurn {
			blocks := message.ContentValue()
			if len(blocks) == 1 {
				if visible, ok := blocks[0].(agentmessage.TextBlock); ok && visible.Text == "User: remember this fact" && message.SourceValue().SourceKind() == "plugin" {
					return
				}
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("Msgcar did not append a Followup to the real child Inbox")
		}
		time.Sleep(time.Millisecond)
	}
}
