package msgcar_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/gorenx/goren/agent"
	agentregistry "github.com/gorenx/goren/agent/registry"
	loopactor "github.com/gorenx/goren/agentloop/actor"
	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/session"
	sessionactor "github.com/gorenx/goren/session/actor"
	"github.com/gorenx/goren/subagent/companion/msgcar"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/log"
)

type sinkReady struct {
	messages chan agentmessage.UserMessage
}

type followupSink struct {
	messages chan agentmessage.UserMessage
}

func (*followupSink) OnActivate(context.Context, *goakt.GrainProps) error   { return nil }
func (*followupSink) OnDeactivate(context.Context, *goakt.GrainProps) error { return nil }
func (owner *followupSink) OnReceive(gc *goakt.GrainContext) {
	switch input := gc.Message().(type) {
	case sinkReady:
		owner.messages = input.messages
		gc.Response(struct{}{})
	case loopactor.Followup:
		owner.messages <- input.Message
		gc.Response(loopactor.Changed{
			Changes: agent.InboxChanges{
				Inserted: []agentmessage.UserMessage{input.Message},
			},
		})
	default:
		gc.Unhandled()
	}
}

func TestMsgcarRelaysOnlyParentUserAndCompletedFinalAgentText(t *testing.T) {
	ctx := t.Context()
	system, err := goakt.NewActorSystem("msgcar-test", goakt.WithPubSub(), goakt.WithLogger(log.DiscardLogger))
	if err != nil {
		t.Fatal(err)
	}
	if err := system.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := system.Stop(context.Background()); err != nil {
			t.Error(err)
		}
	})
	executor, err := goakt.GrainOf[*followupSink](ctx, system, "memory.agentloop", goakt.WithLongLivedGrain())
	if err != nil {
		t.Fatal(err)
	}
	messages := make(chan agentmessage.UserMessage, 8)
	if _, err := system.AskGrain(ctx, executor, sinkReady{
		messages: messages,
	}, time.Second); err != nil {
		t.Fatal(err)
	}
	parent := agentregistry.Creation{
		AgentID:    "parent",
		CreationID: "parent-current",
	}
	child := agentregistry.Creation{
		AgentID:    "memory",
		CreationID: "memory-current",
	}
	if err := msgcar.Start(ctx, system, parent, child, executor); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := msgcar.Stop(context.Background(), system, child); err != nil {
			t.Error(err)
		}
	}()

	user, err := agentmessage.NewUserMessage(agentmessage.UserMessageInput{
		Content: []agentmessage.ContentBlock{agentmessage.NewTextBlock("remember this")},
		Source:  agentmessage.UserMessageSource{},
	})
	if err != nil {
		t.Fatal(err)
	}
	publish(t, system, "other", parent.CreationID, 0, session.UserMessageEventName, user)
	publish(t, system, parent.AgentID, "old", 1, session.UserMessageEventName, user)
	publish(t, system, parent.AgentID, parent.CreationID, 2, session.UserMessageEventName, user)
	publish(t, system, parent.AgentID, parent.CreationID, 2, session.UserMessageEventName, user)
	intermediate, err := agentmessage.NewAssistantMessage(agentmessage.AssistantMessageInput{
		Content: []agentmessage.ContentBlock{
			agentmessage.NewTextBlock("internal work"),
			agentmessage.ToolCallBlock{
				ID:        "call",
				Name:      "search",
				Arguments: "{}",
			},
		},
		Source: agentmessage.ModelMessageSource{
			Provider: "fixture",
			Model:    "fixture",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	publish(t, system, parent.AgentID, parent.CreationID, 3, session.AssistantMessageEventName, session.AssistantMessage{
		Turn:    1,
		Step:    1,
		Message: intermediate,
	})
	final, err := agentmessage.NewAssistantMessage(agentmessage.AssistantMessageInput{
		Content: []agentmessage.ContentBlock{agentmessage.NewTextBlock("final answer")},
		Source: agentmessage.ModelMessageSource{
			Provider: "fixture",
			Model:    "fixture",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	publish(t, system, parent.AgentID, parent.CreationID, 4, session.AssistantMessageEventName, session.AssistantMessage{
		Turn:    1,
		Step:    2,
		Message: final,
	})
	publish(t, system, parent.AgentID, parent.CreationID, 5, session.TurnEndEventName, session.TurnEnd{
		Turn:   1,
		Reason: session.TurnCompleted{},
	})
	publish(t, system, parent.AgentID, parent.CreationID, 6, session.AssistantMessageEventName, session.AssistantMessage{
		Turn:    2,
		Step:    1,
		Message: final,
	})
	publish(t, system, parent.AgentID, parent.CreationID, 7, session.TurnEndEventName, session.TurnEnd{
		Turn: 2,
		Reason: session.TurnAborted{
			Reason: session.UserCancelCause{},
		},
	})
	for _, want := range []string{"User: remember this", "Agent: final answer"} {
		select {
		case got := <-messages:
			blocks := got.ContentValue()
			if len(blocks) != 1 || blocks[0].(agentmessage.TextBlock).Text != want || got.SourceValue().SourceKind() != "plugin" {
				t.Fatalf("unexpected Msgcar Followup: %+v", got)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("missing Followup %q", want)
		}
	}
	select {
	case got := <-messages:
		t.Fatalf("process or aborted output was forwarded: %+v", got)
	case <-time.After(100 * time.Millisecond):
	}
	if err := msgcar.Stop(ctx, system, child); err != nil {
		t.Fatal(err)
	}
	publish(t, system, parent.AgentID, parent.CreationID, 8, session.UserMessageEventName, user)
	select {
	case got := <-messages:
		t.Fatalf("stopped Msgcar forwarded input: %+v", got)
	case <-time.After(100 * time.Millisecond):
	}
}

func publish(t *testing.T, system goakt.ActorSystem, identifier session.SessionID, creationID string, sequence int64, kind string, value any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := goakt.Tell(t.Context(), system.TopicActor(), goakt.NewPublish(uuid.NewString(), session.AppendedEventName, sessionactor.CommitNotice{
		SessionID:  identifier,
		CreationID: creationID,
		Event: session.Event{
			Type: kind,
			Seq:  sequence,
			Data: data,
		},
	})); err != nil {
		t.Fatal(err)
	}
}
