package turnrelay

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/agentmessage"
	pluginruntime "github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/subagent"
	boundcontract "github.com/gorenx/goren/subagent/bound"
)

type relayStore struct {
	session.LiveStore
	conversation session.Context
	onFlush      func(session.Context) error
}

func (store *relayStore) Get(
	identifier session.SessionID,
) (session.Context, bool) {
	if store.conversation == nil || store.conversation.ID() != identifier {
		return nil, false
	}
	return store.conversation, true
}

func (store *relayStore) Flush(
	_ context.Context,
	conversation session.Context,
) error {
	if store.onFlush != nil {
		return store.onFlush(conversation)
	}
	return nil
}

type relayInbox struct {
	deliveries chan boundcontract.Input
}

func (inbox *relayInbox) Deliver(
	_ context.Context,
	_ boundcontract.Address,
	inputValue boundcontract.Input,
) (boundcontract.Receipt, error) {
	inbox.deliveries <- inputValue
	return boundcontract.Receipt{
		InputID:   inputValue.ID,
		MessageID: "child-message",
	}, nil
}

type relayAgent struct {
	conversation session.Context
}

type relayRegistry struct {
	subject agent.Agent
}

func (registry *relayRegistry) Get(
	identifier session.SessionID,
) (agent.Agent, bool) {
	if registry.subject == nil || registry.subject.ID() != identifier {
		return nil, false
	}
	return registry.subject, true
}

func (registry *relayRegistry) Contains(subject agent.Agent) bool {
	return registry.subject != nil && agent.Same(registry.subject, subject)
}

func (registry *relayRegistry) List() []agent.Agent {
	if registry.subject == nil {
		return nil
	}
	return []agent.Agent{registry.subject}
}

func (subject *relayAgent) ID() session.SessionID {
	return subject.conversation.ID()
}

func (*relayAgent) OptionsValue() agent.Options { return agent.Options{} }

func (subject *relayAgent) SessionValue() session.Context {
	return subject.conversation
}

func (*relayAgent) InboxValue() *agent.Inbox { return nil }

func (*relayAgent) StatusValue() agent.Status { return agent.StatusIdle }

func (*relayAgent) Cancel(agent.CancelCause, agent.CancelOptions) {}

func (*relayAgent) WhenIdle(context.Context) error { return nil }

func (*relayAgent) RunMaintenance(
	requestContext context.Context,
	operation func(context.Context) error,
) error {
	return operation(requestContext)
}

func (*relayAgent) Followup(agentmessage.UserMessage) error { return nil }

func (*relayAgent) Steer(agentmessage.UserMessage) error { return nil }

func (*relayAgent) Inject(agentmessage.UserMessage) error { return nil }

func TestSourceCursorReplaysAndAcknowledgesCommittedTurn(
	testingContext *testing.T,
) {
	conversation, bindingValue := newRelaySession(testingContext)
	appendRelayTurnStart(testingContext, conversation, 1)
	appendRelayUser(
		testingContext,
		conversation,
		agentmessage.UserMessageSource{},
		"research this",
	)
	appendRelayAssistant(
		testingContext,
		conversation,
		1,
		[]agentmessage.ContentBlock{
			agentmessage.ReasoningBlock{
				Text: "private reasoning",
			},
			agentmessage.ToolCallBlock{
				ID:        "call-1",
				Name:      "search",
				Arguments: `{}`,
			},
			agentmessage.NewTextBlock("parent answer"),
		},
	)
	appendRelayTurnEnd(
		testingContext,
		conversation,
		1,
		session.TurnCompleted{},
	)
	current := newSourceCursor(
		&relayStore{
			conversation: conversation,
		},
		conversation,
		bindingValue,
	)
	inputValue, found, err := current.next(context.Background())
	if err != nil || !found {
		testingContext.Fatalf("next Input = %#v, %t, %v", inputValue, found, err)
	}
	if hasRelayText(inputValue.Content, "private reasoning") ||
		hasRelayType(inputValue.Content, "tool-call") ||
		!hasRelayText(inputValue.Content, "research this") ||
		!hasRelayText(inputValue.Content, "parent answer") {
		testingContext.Fatalf("Input = %#v", inputValue)
	}
	if err = current.acknowledge(
		context.Background(),
		boundcontract.Receipt{
			InputID:   inputValue.ID,
			MessageID: "child-message",
		},
	); err != nil {
		testingContext.Fatal(err)
	}
	nextSeq, err := cursorPosition(conversation.Events(), bindingValue)
	if err != nil || nextSeq != 5 {
		testingContext.Fatalf("cursor = %d, error = %v", nextSeq, err)
	}
}

func TestSourceCursorSkipsNonUserTurnAndReplaysUnacknowledgedInput(
	testingContext *testing.T,
) {
	conversation, bindingValue := newRelaySession(testingContext)
	appendRelayTurnStart(testingContext, conversation, 1)
	appendRelayUser(
		testingContext,
		conversation,
		subagent.ReportSource{
			SenderSessionID: "child",
		},
		"child report",
	)
	appendRelayTurnEnd(
		testingContext,
		conversation,
		1,
		session.TurnCompleted{},
	)
	appendRelayTurnStart(testingContext, conversation, 2)
	appendRelayUser(
		testingContext,
		conversation,
		agentmessage.UserMessageSource{},
		"direct user",
	)
	appendRelayTurnEnd(
		testingContext,
		conversation,
		2,
		session.TurnBlocked{},
	)
	store := &relayStore{
		conversation: conversation,
	}
	first := newSourceCursor(store, conversation, bindingValue)
	inputValue, found, err := first.next(context.Background())
	if err != nil || !found ||
		!hasRelayText(inputValue.Content, "direct user") {
		testingContext.Fatalf("first Input = %#v, %t, %v", inputValue, found, err)
	}
	second := newSourceCursor(store, conversation, bindingValue)
	replayed, found, err := second.next(context.Background())
	if err != nil || !found || replayed.ID != inputValue.ID {
		testingContext.Fatalf(
			"replayed Input = %#v, %t, %v",
			replayed,
			found,
			err,
		)
	}
	receiptValue := boundcontract.Receipt{
		InputID:   inputValue.ID,
		MessageID: "child-message",
	}
	if err = first.acknowledge(
		context.Background(),
		receiptValue,
	); err != nil {
		testingContext.Fatal(err)
	}
	if err = second.acknowledge(
		context.Background(),
		receiptValue,
	); err != nil {
		testingContext.Fatalf("duplicate acknowledgement = %v", err)
	}
	cursors := 0
	for _, committed := range conversation.Events() {
		if committed.Type == cursorEventName {
			cursors++
		}
	}
	if cursors != 2 {
		testingContext.Fatalf("cursor events = %d, want skipped + delivered", cursors)
	}
}

func TestSourceCursorDoesNotExposeInputBeforeSourceFlush(
	testingContext *testing.T,
) {
	conversation, bindingValue := newRelaySession(testingContext)
	appendRelayTurnStart(testingContext, conversation, 1)
	appendRelayUser(
		testingContext,
		conversation,
		agentmessage.UserMessageSource{},
		"durable first",
	)
	appendRelayTurnEnd(
		testingContext,
		conversation,
		1,
		session.TurnCompleted{},
	)
	sentinel := errors.New("source flush failed")
	current := newSourceCursor(
		&relayStore{
			conversation: conversation,
			onFlush: func(session.Context) error {
				return sentinel
			},
		},
		conversation,
		bindingValue,
	)
	inputValue, found, err := current.next(context.Background())
	if !errors.Is(err, sentinel) || found || inputValue.ID != "" ||
		current.pending != nil {
		testingContext.Fatalf(
			"Input = %#v, found = %t, pending = %#v, error = %v",
			inputValue,
			found,
			current.pending,
			err,
		)
	}
}

func TestWorkerRelaysTurnAfterSessionWakeup(testingContext *testing.T) {
	conversation, _ := newRelaySession(testingContext)
	targetInbox := &relayInbox{
		deliveries: make(chan boundcontract.Input, 1),
	}
	owner := New(Diagnostics{})
	owner.ctx, owner.cancel = context.WithCancel(context.Background())
	owner.store = &relayStore{
		conversation: conversation,
	}
	owner.inbox = targetInbox
	parentAgent := &relayAgent{
		conversation: conversation,
	}
	owner.agents = &relayRegistry{
		subject: parentAgent,
	}
	appendRelayTurnStart(testingContext, conversation, 1)
	appendRelayUser(
		testingContext,
		conversation,
		agentmessage.UserMessageSource{},
		"wake up",
	)
	appendRelayTurnEnd(
		testingContext,
		conversation,
		1,
		session.TurnCompleted{},
	)
	entries := conversation.Events()
	if err := owner.ObserveEvent(
		context.Background(),
		session.EventAppended{
			Conversation: conversation,
			Committed:    entries[len(entries)-1],
		},
	); err != nil {
		testingContext.Fatal(err)
	}
	select {
	case inputValue := <-targetInbox.deliveries:
		if !hasRelayText(inputValue.Content, "wake up") {
			testingContext.Fatalf("Input = %#v", inputValue)
		}
	case <-time.After(time.Second):
		testingContext.Fatal("worker did not relay the committed turn")
	}
	if err := owner.Dispose(context.Background()); err != nil {
		testingContext.Fatal(err)
	}
}

func newRelaySession(
	testingContext *testing.T,
) (session.Context, binding) {
	testingContext.Helper()
	conversation, err := session.New("parent", session.CreateOptions{})
	if err != nil {
		testingContext.Fatal(err)
	}
	draft, err := session.NewEventDraft(
		boundcontract.BindingEvent,
		boundcontract.BindingData{
			Version:        boundcontract.EventVersion,
			Name:           "researcher",
			ChildSessionID: "child",
			ContextNextSeq: 0,
		},
	)
	if err != nil {
		testingContext.Fatal(err)
	}
	commitRelayDraft(testingContext, conversation, draft)
	bindings, err := sessionBindings(conversation.Events(), conversation.ID())
	if err != nil || len(bindings) != 1 {
		testingContext.Fatalf("Bindings = %#v, error = %v", bindings, err)
	}
	return conversation, bindings[0]
}

func appendRelayTurnStart(
	testingContext *testing.T,
	conversation session.Context,
	turnNumber int64,
) {
	testingContext.Helper()
	draft, err := session.NewEventDraft(
		session.TurnStarted,
		session.TurnStart{
			Turn: turnNumber,
		},
	)
	if err != nil {
		testingContext.Fatal(err)
	}
	commitRelayDraft(testingContext, conversation, draft)
}

func appendRelayUser(
	testingContext *testing.T,
	conversation session.Context,
	origin agentmessage.MessageSource,
	text string,
) {
	testingContext.Helper()
	messageValue, err := agentmessage.NewUserMessage(
		agentmessage.UserMessageInput{
			Content: []agentmessage.ContentBlock{
				agentmessage.NewTextBlock(text),
			},
			Source: origin,
		},
	)
	if err != nil {
		testingContext.Fatal(err)
	}
	draft, err := session.NewSurfaceEventDraft(
		session.UserMessageAdded,
		messageValue,
		session.SurfaceIntent{
			Operation: session.SurfaceAppend(),
		},
	)
	if err != nil {
		testingContext.Fatal(err)
	}
	commitRelayDraft(testingContext, conversation, draft)
}

func appendRelayAssistant(
	testingContext *testing.T,
	conversation session.Context,
	turnNumber int64,
	content []agentmessage.ContentBlock,
) {
	testingContext.Helper()
	messageValue, err := agentmessage.NewAssistantMessage(
		agentmessage.AssistantMessageInput{
			Content: content,
			Source: agentmessage.ModelMessageSource{
				Provider: "provider",
				Model:    "model",
			},
		},
	)
	if err != nil {
		testingContext.Fatal(err)
	}
	draft, err := session.NewSurfaceEventDraft(
		session.AssistantMessaged,
		session.AssistantMessage{
			Turn:    turnNumber,
			Step:    1,
			Message: messageValue,
		},
		session.SurfaceIntent{
			Operation: session.SurfaceAppend(),
		},
	)
	if err != nil {
		testingContext.Fatal(err)
	}
	commitRelayDraft(testingContext, conversation, draft)
}

func appendRelayTurnEnd(
	testingContext *testing.T,
	conversation session.Context,
	turnNumber int64,
	reason session.TurnEndReason,
) {
	testingContext.Helper()
	draft, err := session.NewEventDraft(
		session.TurnEnded,
		session.TurnEnd{
			Turn:   turnNumber,
			Reason: reason,
		},
	)
	if err != nil {
		testingContext.Fatal(err)
	}
	commitRelayDraft(testingContext, conversation, draft)
}

func commitRelayDraft(
	testingContext *testing.T,
	conversation session.Context,
	draft session.EventDraft,
) {
	testingContext.Helper()
	if _, err := conversation.Commit(
		context.Background(),
		session.Batch(draft),
	); err != nil {
		testingContext.Fatal(err)
	}
}

func hasRelayText(
	content []agentmessage.ContentBlock,
	want string,
) bool {
	for _, block := range content {
		plain, matches := block.(agentmessage.PlainTextContent)
		if !matches {
			continue
		}
		text, present := plain.PlainText()
		if present && text == want {
			return true
		}
	}
	return false
}

func hasRelayType(
	content []agentmessage.ContentBlock,
	want string,
) bool {
	for _, block := range content {
		if block.ContentType() == want {
			return true
		}
	}
	return false
}

var _ agent.Agent = (*relayAgent)(nil)
var _ boundcontract.Inbox = (*relayInbox)(nil)

func TestManifestConsumesInboxAndOwnsSessionObservation(
	testingContext *testing.T,
) {
	testingContext.Parallel()
	pluginManifest := New(Diagnostics{}).Manifest()
	if len(pluginManifest.Provides) != 0 {
		testingContext.Fatalf("provided Services = %#v", pluginManifest.Provides)
	}
	// Key is a required capability type name. Value records set membership.
	required := make(map[string]bool)
	for _, capability := range pluginManifest.Requires {
		required[capability.Name()] = true
	}
	for _, capability := range []pluginruntime.ServiceType{
		pluginruntime.ServiceOf[agent.Registry](),
		pluginruntime.ServiceOf[session.LiveStore](),
		pluginruntime.ServiceOf[boundcontract.Inbox](),
	} {
		if !required[capability.Name()] {
			testingContext.Fatalf(
				"Turn Relay does not require %q",
				capability.Name(),
			)
		}
	}
	// Key is an observed event type name. Value records set membership.
	events := make(map[string]bool)
	for _, eventType := range pluginManifest.Events {
		events[eventType.Name()] = true
	}
	for _, eventName := range []string{
		(agent.SessionStarted{}).EventName(),
		(agent.Disposed{}).EventName(),
		(session.EventAppended{}).EventName(),
	} {
		if !events[eventName] {
			testingContext.Fatalf("Turn Relay does not observe %q", eventName)
		}
	}
}
