package bound

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/session/persistence"
	sessionprojection "github.com/gorenx/goren/session/projection"
	"github.com/gorenx/goren/subagent"
	sharedexecution "github.com/gorenx/goren/subagent/internal/execution"
	subagentprojection "github.com/gorenx/goren/subagent/internal/projection"
)

type boundRuntimePersistence struct {
	persistence.Persistence
	mutex       sync.Mutex
	inspections map[session.SessionID]persistence.Inspection
}

func (source *boundRuntimePersistence) Inspect(
	_ context.Context,
	identifier session.SessionID,
) (persistence.Inspection, error) {
	source.mutex.Lock()
	defer source.mutex.Unlock()
	inspection, found := source.inspections[identifier]
	if !found {
		return persistence.Inspection{}, &persistence.NotFoundError{
			ID: identifier,
		}
	}
	return cloneInspection(inspection), nil
}

func (source *boundRuntimePersistence) store(
	conversation session.Context,
) {
	source.mutex.Lock()
	source.inspections[conversation.ID()] = persistence.Inspection{
		Header: conversation.Header(),
		Events: conversation.Events(),
	}
	source.mutex.Unlock()
}

type boundRuntimeSessions struct {
	session.LiveStore
	mutex       sync.Mutex
	entries     map[session.SessionID]session.Context
	persistence *boundRuntimePersistence
}

func (source *boundRuntimeSessions) Get(
	identifier session.SessionID,
) (session.Context, bool) {
	source.mutex.Lock()
	conversation, found := source.entries[identifier]
	source.mutex.Unlock()
	return conversation, found
}

func (source *boundRuntimeSessions) Flush(
	_ context.Context,
	conversation session.Context,
) error {
	if conversation == nil {
		return errors.New("test: flush Session is nil")
	}
	source.persistence.store(conversation)
	return nil
}

func (source *boundRuntimeSessions) enter(
	conversation session.Context,
) {
	source.mutex.Lock()
	source.entries[conversation.ID()] = conversation
	source.mutex.Unlock()
}

func (source *boundRuntimeSessions) leave(
	identifier session.SessionID,
) {
	source.mutex.Lock()
	delete(source.entries, identifier)
	source.mutex.Unlock()
}

type boundRuntimeAgent struct {
	identifier   session.SessionID
	conversation session.Context
	options      agent.Options
	mutex        sync.Mutex
	followups    int
}

func (subject *boundRuntimeAgent) ID() session.SessionID {
	return subject.identifier
}

func (subject *boundRuntimeAgent) OptionsValue() agent.Options {
	return subject.options
}

func (subject *boundRuntimeAgent) SessionValue() session.Context {
	return subject.conversation
}

func (*boundRuntimeAgent) InboxValue() *agent.Inbox {
	return nil
}

func (*boundRuntimeAgent) StatusValue() agent.Status {
	return agent.StatusIdle
}

func (*boundRuntimeAgent) Cancel(agent.CancelCause, agent.CancelOptions) {}

func (*boundRuntimeAgent) WhenIdle(context.Context) error {
	return nil
}

func (*boundRuntimeAgent) RunMaintenance(
	ctx context.Context,
	maintenanceAction func(context.Context) error,
) error {
	return maintenanceAction(ctx)
}

func (subject *boundRuntimeAgent) Followup(
	messageValue agentmessage.UserMessage,
) error {
	draft, err := session.NewEventDraft(
		agent.InboxSpliced,
		agent.InboxSplice{
			Target: agent.NextTurn,
			Inserted: []agentmessage.UserMessage{
				messageValue,
			},
		},
	)
	if err != nil {
		return err
	}
	if _, err = subject.conversation.Commit(
		context.Background(),
		session.Batch(draft),
	); err != nil {
		return err
	}
	subject.mutex.Lock()
	subject.followups++
	subject.mutex.Unlock()
	return nil
}

func (*boundRuntimeAgent) Steer(agentmessage.UserMessage) error {
	return nil
}

func (*boundRuntimeAgent) Inject(agentmessage.UserMessage) error {
	return nil
}

type boundRuntimeScope struct {
	subject   agent.Agent
	resources []agent.ScopeResource
}

func (scope *boundRuntimeScope) Agent() agent.Agent {
	return scope.subject
}

func (scope *boundRuntimeScope) Own(resource agent.ScopeResource) error {
	if resource == nil {
		return errors.New("test: nil Scope resource")
	}
	scope.resources = append(scope.resources, resource)
	return nil
}

type boundAgentRuntime struct {
	scope    *boundRuntimeScope
	sessions *boundRuntimeSessions
}

func (*boundAgentRuntime) Dispatch(
	context.Context,
	agent.RuntimeEvent,
) error {
	return nil
}

func (*boundAgentRuntime) ResolvePreStep(
	ctx context.Context,
	notice agent.PreStepNotice,
	action agent.PreStepAction,
) (agent.PreStepDecision, error) {
	return action.Execute(ctx, notice)
}

func (*boundAgentRuntime) ResolveRequest(
	ctx context.Context,
	notice agent.RequestNotice,
	action agent.RequestAction,
) (agent.RequestResolution, error) {
	return action.Execute(ctx, notice)
}

func (*boundAgentRuntime) ResolveRequestError(
	ctx context.Context,
	notice agent.RequestErrorNotice,
	handler agent.RequestErrorHandler,
) (agent.RequestErrorAction, error) {
	return handler.Execute(ctx, notice)
}

func (runtime *boundAgentRuntime) Provision(
	ctx context.Context,
	source agent.Provisioner,
) error {
	return agent.ApplyProvisioning(ctx, runtime.scope, source)
}

func (runtime *boundAgentRuntime) Teardown(ctx context.Context) error {
	var closeErr error
	for index := len(runtime.scope.resources) - 1; index >= 0; index-- {
		closeErr = errors.Join(
			closeErr,
			runtime.scope.resources[index].Dispose(ctx),
		)
	}
	runtime.sessions.leave(runtime.scope.subject.ID())
	return closeErr
}

type boundAgentFactory struct {
	sessions     *boundRuntimeSessions
	persistence  *boundRuntimePersistence
	mutex        sync.Mutex
	createCalls  int
	resumeCalls  int
	createErrors map[session.SessionID]error
	latestAgents map[session.SessionID]*boundRuntimeAgent
}

func (builder *boundAgentFactory) CreateAgent(
	ctx context.Context,
	agentEpoch agent.AgentEpoch,
	options agent.CreateOptions,
) error {
	conversation, err := session.New(
		options.SessionID,
		session.CreateOptions{
			Seed:     options.Seed,
			Metadata: options.Metadata,
		},
	)
	if err != nil {
		return err
	}
	builder.mutex.Lock()
	builder.createCalls++
	createErr := builder.createErrors[options.SessionID]
	builder.mutex.Unlock()
	if createErr != nil {
		return createErr
	}
	return builder.attach(
		ctx,
		agentEpoch,
		conversation,
		options.AgentOptions,
		options.Provisioner,
	)
}

func (builder *boundAgentFactory) ResumeAgent(
	ctx context.Context,
	agentEpoch agent.AgentEpoch,
	options agent.ResumeOptions,
) error {
	inspection, err := builder.persistence.Inspect(ctx, options.SessionID)
	if err != nil {
		return err
	}
	conversation, err := session.New(
		options.SessionID,
		session.CreateOptions{
			Seed: inspection.Events,
			Metadata: session.Metadata{
				CreatedAt:       int64Pointer(inspection.Header.CreatedAt),
				CWD:             inspection.Header.CWD,
				ParentSession:   inspection.Header.ParentSession,
				SeedLength:      inspection.Header.SeedLength,
				Origin:          inspection.Header.Origin,
				DelegationDepth: inspection.Header.DelegationDepth,
				AgentPreset:     inspection.Header.AgentPreset,
			},
		},
	)
	if err != nil {
		return err
	}
	builder.mutex.Lock()
	builder.resumeCalls++
	builder.mutex.Unlock()
	return builder.attach(
		ctx,
		agentEpoch,
		conversation,
		options.AgentOptions,
		options.Provisioner,
	)
}

func (builder *boundAgentFactory) attach(
	ctx context.Context,
	agentEpoch agent.AgentEpoch,
	conversation session.Context,
	options agent.Options,
	source agent.Provisioner,
) error {
	subject := &boundRuntimeAgent{
		identifier:   conversation.ID(),
		conversation: conversation,
		options:      options,
	}
	scope := &boundRuntimeScope{
		subject: subject,
	}
	if source != nil {
		if err := agent.ApplyProvisioning(ctx, scope, source); err != nil {
			return err
		}
	}
	runtime := &boundAgentRuntime{
		scope:    scope,
		sessions: builder.sessions,
	}
	if _, err := agentEpoch.Attach(subject, runtime); err != nil {
		return err
	}
	builder.sessions.enter(conversation)
	builder.mutex.Lock()
	builder.latestAgents[conversation.ID()] = subject
	builder.mutex.Unlock()
	return nil
}

type boundRuntimeFailures struct {
	mutex            sync.Mutex
	materializations []MaterializationFailure
	flushes          []FinalFlushFailure
	interactions     []InteractionFailure
}

type boundRuntimeExtensions struct{}

func (*boundRuntimeExtensions) Validate([]string) error {
	return nil
}

func (*boundRuntimeExtensions) Provision(
	[]string,
) (agent.Provisioner, error) {
	return nil, nil
}

func (reporter *boundRuntimeFailures) ReportBoundMaterializationFailure(
	failure MaterializationFailure,
) {
	reporter.mutex.Lock()
	reporter.materializations = append(reporter.materializations, failure)
	reporter.mutex.Unlock()
}

func (reporter *boundRuntimeFailures) ReportBoundFinalFlushFailure(
	failure FinalFlushFailure,
) {
	reporter.mutex.Lock()
	reporter.flushes = append(reporter.flushes, failure)
	reporter.mutex.Unlock()
}

func (reporter *boundRuntimeFailures) ReportBoundInteractionFailure(
	failure InteractionFailure,
) {
	reporter.mutex.Lock()
	reporter.interactions = append(reporter.interactions, failure)
	reporter.mutex.Unlock()
}

type boundRuntimeFixture struct {
	owner        *Service
	agents       *agent.RegistryService
	agentFactory *boundAgentFactory
	failures     *boundRuntimeFailures
	parent       agent.Handle
	projections  *sessionprojection.DriveRegistry
}

func newBoundRuntimeFixture(t *testing.T) boundRuntimeFixture {
	t.Helper()
	persistenceSource := &boundRuntimePersistence{
		inspections: make(map[session.SessionID]persistence.Inspection),
	}
	sessions := &boundRuntimeSessions{
		entries:     make(map[session.SessionID]session.Context),
		persistence: persistenceSource,
	}
	agentRegistry := agent.NewRegistry(agent.RegistryOptions{})
	agentFactory := &boundAgentFactory{
		sessions:     sessions,
		persistence:  persistenceSource,
		createErrors: make(map[session.SessionID]error),
		latestAgents: make(map[session.SessionID]*boundRuntimeAgent),
	}
	registration, err := agentRegistry.RegisterFactory(agentFactory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(registration.Close)
	parentHandle, err := agentRegistry.Create(
		context.Background(),
		agent.CreateOptions{
			SessionID: "parent",
			AgentOptions: agent.Options{
				Provider: "provider",
				Model:    "model",
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	projections := sessionprojection.NewDriveRegistry()
	for _, unit := range subagentprojection.Units() {
		if _, err = projections.Register(unit); err != nil {
			t.Fatal(err)
		}
	}
	failures := &boundRuntimeFailures{}
	extensionSource := &boundRuntimeExtensions{}
	owner, err := New(
		Dependencies{
			Agents:      agentRegistry,
			Constructor: agentRegistry,
			Sessions:    sessions,
			Persistence: persistenceSource,
			Projections: projections,
			SeedBuilders: &boundConfigSeedRegistry{
				builders: map[string]subagent.SeedBuilder{
					"spawn": &boundConfigSeedBuilder{
						name: "spawn",
					},
				},
			},
			Extensions: extensionSource,
			Executions: sharedexecution.NewRegistry(),
			Failures:   failures,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	return boundRuntimeFixture{
		owner:        owner,
		agents:       agentRegistry,
		agentFactory: agentFactory,
		failures:     failures,
		parent:       parentHandle,
		projections:  projections,
	}
}

func TestStartBindingsContainsChildMaterializationFailure(t *testing.T) {
	fixture := newBoundRuntimeFixture(t)
	childID := bindRuntimeChild(t, fixture)
	sentinel := errors.New("test: child create failed")
	fixture.agentFactory.mutex.Lock()
	fixture.agentFactory.createErrors[childID] = sentinel
	fixture.agentFactory.mutex.Unlock()
	if err := fixture.owner.StartBindings(
		context.Background(),
		fixture.parent.Subject,
	); err != nil {
		t.Fatalf("StartBindings returned child failure: %v", err)
	}
	if !fixture.agents.Contains(fixture.parent.Subject) {
		t.Fatal("child materialization failure removed the parent Agent")
	}
	projectionSnapshot, err := fixture.projections.Snapshot(
		fixture.parent.Subject.SessionValue(),
	)
	if err != nil {
		t.Fatal(err)
	}
	view, found, err := subagentprojection.ReadBound(projectionSnapshot.Values)
	if err != nil {
		t.Fatal(err)
	}
	if !found || len(view.Materializations) != 1 ||
		view.Materializations[0].ChildSessionID != childID ||
		view.Materializations[0].Result != subagent.BoundMaterializationFailed {
		t.Fatalf("materialization state = %#v, found = %v", view, found)
	}
	fixture.failures.mutex.Lock()
	materializationFailures := append(
		[]MaterializationFailure(nil),
		fixture.failures.materializations...,
	)
	fixture.failures.mutex.Unlock()
	if len(materializationFailures) != 1 ||
		materializationFailures[0].ParentID != fixture.parent.Subject.ID() ||
		materializationFailures[0].ChildID != childID ||
		!errors.Is(materializationFailures[0].Error, sentinel) {
		t.Fatalf("materialization diagnostics = %#v", materializationFailures)
	}
}

func TestHasSubmittedMessageIgnoresInheritedSeedInbox(t *testing.T) {
	t.Parallel()
	seedSplice, err := json.Marshal(
		agent.InboxSplice{
			Target: agent.NextTurn,
			Inserted: []agentmessage.UserMessage{
				newBoundRuntimeMessage(t, "inherited parent work"),
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	childSplice, err := json.Marshal(
		agent.InboxSplice{
			Target: agent.NextTurn,
			Inserted: []agentmessage.UserMessage{
				newBoundRuntimeMessage(t, "child initial prompt"),
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	seedLength := int64(1)
	inspection := persistence.Inspection{
		Header: session.Header{
			SeedLength: &seedLength,
		},
		Events: []session.Event{
			{
				Type: agent.InboxSplicedEventName,
				Seq:  0,
				Data: seedSplice,
			},
		},
	}
	if hasSubmittedMessage(inspection) {
		t.Fatal("inherited parent Inbox was treated as the child initial prompt")
	}
	inspection.Events = append(
		inspection.Events,
		session.Event{
			Type: agent.InboxSplicedEventName,
			Seq:  1,
			Data: childSplice,
		},
	)
	if !hasSubmittedMessage(inspection) {
		t.Fatal("child Inbox after the seed boundary was not detected")
	}
}

func TestBoundFreshCreateAndColdResumeSubmitInitialPromptOnce(t *testing.T) {
	fixture := newBoundRuntimeFixture(t)
	childID := bindRuntimeChild(t, fixture)
	first := startRuntimeChild(t, fixture, childID)
	firstAgent, found := fixture.agents.Get(childID)
	if !found {
		t.Fatal("fresh Bound child is not resident")
	}
	if countSubmittedMessages(firstAgent.SessionValue().Events()) != 1 {
		t.Fatal("fresh Bound child did not submit exactly one initial prompt")
	}
	assertAppliedRevision(t, fixture.projections, firstAgent, 1, 1)
	if err := first.Dispose(context.Background()); err != nil {
		t.Fatal(err)
	}
	second := startRuntimeChild(t, fixture, childID)
	secondAgent, found := fixture.agents.Get(childID)
	if !found || agent.Same(firstAgent, secondAgent) {
		t.Fatal("cold resume did not create a new exact Agent epoch")
	}
	if countSubmittedMessages(secondAgent.SessionValue().Events()) != 1 {
		t.Fatal("cold resume duplicated the initial prompt")
	}
	assertAppliedRevision(t, fixture.projections, secondAgent, 1, 2)
	fixture.agentFactory.mutex.Lock()
	createCalls := fixture.agentFactory.createCalls
	resumeCalls := fixture.agentFactory.resumeCalls
	fixture.agentFactory.mutex.Unlock()
	if createCalls != 2 || resumeCalls != 1 {
		t.Fatalf(
			"factory calls = create:%d resume:%d, want create:2 resume:1",
			createCalls,
			resumeCalls,
		)
	}
	if err := second.Dispose(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestBoundConfigRevisionReplacesResidentEpochAndDisableStopsIt(
	t *testing.T,
) {
	fixture := newBoundRuntimeFixture(t)
	childID := bindRuntimeChild(t, fixture)
	first := startRuntimeChild(t, fixture, childID)
	firstAgent, _ := fixture.agents.Get(childID)
	result, err := fixture.owner.UpdateConfig(
		context.Background(),
		subagent.UpdateBoundConfigCommand{
			Parent:           fixture.parent.Subject,
			ChildSessionID:   childID,
			ExpectedRevision: 1,
			Config: subagent.BoundConfigInput{
				Enabled: true,
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.Revision != 2 || first.State() != subagent.ExecutionStopped {
		t.Fatalf("replacement result = %#v, first state = %s", result, first.State())
	}
	secondAgent, found := fixture.agents.Get(childID)
	if !found || agent.Same(firstAgent, secondAgent) {
		t.Fatal("config revision did not replace the exact Agent epoch")
	}
	if countSubmittedMessages(secondAgent.SessionValue().Events()) != 1 {
		t.Fatal("config replacement duplicated the initial prompt")
	}
	assertAppliedRevision(t, fixture.projections, secondAgent, 2, 2)
	result, err = fixture.owner.UpdateConfig(
		context.Background(),
		subagent.UpdateBoundConfigCommand{
			Parent:           fixture.parent.Subject,
			ChildSessionID:   childID,
			ExpectedRevision: 2,
			Config: subagent.BoundConfigInput{
				Enabled: false,
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.Revision != 3 {
		t.Fatalf("disabled revision = %d", result.Revision)
	}
	if _, found = fixture.agents.Get(childID); found {
		t.Fatal("disabled Bound config retained a resident Agent epoch")
	}
	messageValue, err := agentmessage.NewUserMessage(
		agentmessage.UserMessageInput{
			Content: []agentmessage.ContentBlock{
				agentmessage.NewTextBlock("later work"),
			},
			Source: agentmessage.UserMessageSource{
				Kind: "user",
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	_, err = fixture.owner.Send(
		context.Background(),
		fixture.parent.Subject,
		childID,
		messageValue,
	)
	var typed *subagent.Error
	if !errors.As(err, &typed) || typed.Code != subagent.ErrorBoundDisabled {
		t.Fatalf("disabled Send error = %v", err)
	}
}

func TestBoundWorkerDeliversCompletedParentInteractionOnce(t *testing.T) {
	fixture := newBoundRuntimeFixture(t)
	childID := bindRuntimeChild(t, fixture)
	_ = startRuntimeChild(t, fixture, childID)
	defer func() {
		_ = fixture.owner.Close(context.Background())
	}()
	parentSession := fixture.parent.Subject.SessionValue()
	appendTurnStart(t, parentSession, 1)
	appendInteractionUser(
		t,
		parentSession,
		agentmessage.UserMessageSource{},
		"investigate this",
	)
	appendInteractionAssistant(
		t,
		parentSession,
		1,
		1,
		[]agentmessage.ContentBlock{
			agentmessage.NewTextBlock("parent response"),
		},
	)
	appendTurnEnd(t, parentSession, 1, session.TurnCompleted{})
	ended := parentSession.Events()[parentSession.Seq()-1]
	fixture.owner.SessionEventAppended(session.EventAppended{
		Conversation: parentSession,
		Committed:    ended,
	})
	waitForBoundCondition(t, func() bool {
		return latestBoundCursor(parentSession, childID) != nil
	})
	childAgent := fixture.agentFactory.latestAgents[childID]
	if followupCount(childAgent) != 2 {
		t.Fatalf(
			"child followups = %d, want initial prompt plus one interaction",
			followupCount(childAgent),
		)
	}
	cursor := latestBoundCursor(parentSession, childID)
	if cursor == nil || cursor.Disposition != subagent.BoundCursorDelivered ||
		cursor.ThroughTurn != 1 {
		t.Fatalf("Bound cursor = %#v", cursor)
	}
	delivery := boundDelivery(fixture.owner, fixture.parent.Subject.ID(), childID)
	if delivery == nil {
		t.Fatal("Bound interaction delivery is missing")
	}
	if err := delivery.catchUp(); err != nil {
		t.Fatal(err)
	}
	if followupCount(childAgent) != 2 {
		t.Fatal("cursor catch-up duplicated the child interaction receipt")
	}
}

func TestBoundWorkerRepairsCursorFromDurableChildReceipt(t *testing.T) {
	fixture := newBoundRuntimeFixture(t)
	childID := bindRuntimeChild(t, fixture)
	_ = startRuntimeChild(t, fixture, childID)
	defer func() {
		_ = fixture.owner.Close(context.Background())
	}()
	delivery := detachBoundDelivery(
		fixture.owner,
		fixture.parent.Subject.ID(),
		childID,
	)
	if delivery == nil {
		t.Fatal("Bound interaction delivery is missing")
	}
	delivery.Stop()
	parentSession := fixture.parent.Subject.SessionValue()
	appendTurnStart(t, parentSession, 1)
	appendInteractionUser(
		t,
		parentSession,
		agentmessage.UserMessageSource{},
		"recover this",
	)
	appendTurnEnd(t, parentSession, 1, session.TurnBlocked{})
	interaction, found, err := nextParentInteraction(
		parentSession.Events(),
		delivery.floor,
	)
	if err != nil || !found || !interaction.deliverable {
		t.Fatalf("interaction = %#v, found = %v, error = %v", interaction, found, err)
	}
	childAgent := fixture.agentFactory.latestAgents[childID]
	messageSource := subagent.Delivery{
		ParentSessionID: fixture.parent.Subject.ID(),
		Turn:            interaction.turn,
		FromSeq:         interaction.fromSeq,
		ThroughSeq:      interaction.nextSeq - 1,
		Outcome:         interaction.outcome,
	}
	messageValue, err := agentmessage.NewUserMessage(
		agentmessage.UserMessageInput{
			Content: interaction.content,
			Source:  messageSource,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err = childAgent.Followup(messageValue); err != nil {
		t.Fatal(err)
	}
	if err = fixture.owner.dependencies.Sessions.Flush(
		context.Background(),
		childAgent.SessionValue(),
	); err != nil {
		t.Fatal(err)
	}
	recovery := newInteractionDelivery(
		fixture.owner.interactions,
		fixture.parent.Subject,
		subagentprojection.BoundBinding{
			ChildSessionID: childID,
			Seq:            delivery.floor - 1,
		},
		childAgent.SessionValue(),
		delivery.slot,
	)
	defer recovery.cancel()
	advanced, err := recovery.advanceOne()
	if err != nil || !advanced {
		t.Fatalf("recovery advance = %v, error = %v", advanced, err)
	}
	if followupCount(childAgent) != 2 {
		t.Fatal("receipt recovery called Followup a second time")
	}
	cursor := latestBoundCursor(parentSession, childID)
	if cursor == nil || cursor.Disposition != subagent.BoundCursorDelivered {
		t.Fatalf("repaired Bound cursor = %#v", cursor)
	}
}

func TestBoundWorkerKeepsBacklogWhileDisabledAndCatchesUpAfterEnable(
	t *testing.T,
) {
	fixture := newBoundRuntimeFixture(t)
	childID := bindRuntimeChild(t, fixture)
	_ = startRuntimeChild(t, fixture, childID)
	defer func() {
		_ = fixture.owner.Close(context.Background())
	}()
	_, err := fixture.owner.UpdateConfig(
		context.Background(),
		subagent.UpdateBoundConfigCommand{
			Parent:           fixture.parent.Subject,
			ChildSessionID:   childID,
			ExpectedRevision: 1,
			Config: subagent.BoundConfigInput{
				Enabled: false,
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	parentSession := fixture.parent.Subject.SessionValue()
	appendTurnStart(t, parentSession, 1)
	appendInteractionUser(
		t,
		parentSession,
		agentmessage.UserMessageSource{},
		"queued while disabled",
	)
	appendTurnEnd(t, parentSession, 1, session.TurnCompleted{})
	delivery := boundDelivery(fixture.owner, fixture.parent.Subject.ID(), childID)
	if delivery == nil {
		t.Fatal("disabled binding lost its interaction delivery")
	}
	if err = delivery.catchUp(); err != nil {
		t.Fatal(err)
	}
	if latestBoundCursor(parentSession, childID) != nil {
		t.Fatal("disabled Bound delivery advanced its cursor")
	}
	_, err = fixture.owner.UpdateConfig(
		context.Background(),
		subagent.UpdateBoundConfigCommand{
			Parent:           fixture.parent.Subject,
			ChildSessionID:   childID,
			ExpectedRevision: 2,
			Config: subagent.BoundConfigInput{
				Enabled: true,
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	_ = startRuntimeChild(t, fixture, childID)
	delivery = boundDelivery(fixture.owner, fixture.parent.Subject.ID(), childID)
	if delivery == nil {
		t.Fatal("re-enabled binding lost its interaction delivery")
	}
	waitForBoundCondition(t, func() bool {
		return latestBoundCursor(parentSession, childID) != nil
	})
	resumed := fixture.agentFactory.latestAgents[childID]
	if followupCount(resumed) != 1 {
		t.Fatalf("resumed child followups = %d, want backlog once", followupCount(resumed))
	}
}

func bindRuntimeChild(
	t *testing.T,
	fixture boundRuntimeFixture,
) session.SessionID {
	t.Helper()
	childID := session.SessionID("bound-child")
	_, err := fixture.owner.Bind(
		context.Background(),
		subagent.BindCommand{
			Parent:           fixture.parent.Subject,
			RequestedChildID: &childID,
			SeedBuilder:      "spawn",
			Title:            "researcher",
			InitialPrompt: []agentmessage.ContentBlock{
				agentmessage.NewTextBlock("initial task"),
			},
			Config: subagent.BoundConfigInput{
				Enabled: true,
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	return childID
}

func startRuntimeChild(
	t *testing.T,
	fixture boundRuntimeFixture,
	childID session.SessionID,
) subagent.Execution {
	t.Helper()
	command, err := subagent.NewBoundStart(
		fixture.parent.Subject,
		childID,
	)
	if err != nil {
		t.Fatal(err)
	}
	running, err := fixture.owner.Start(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	return running
}

func assertAppliedRevision(
	t *testing.T,
	projections *sessionprojection.DriveRegistry,
	subject agent.Agent,
	wantRevision int64,
	wantCount int,
) {
	t.Helper()
	projectionSnapshot, err := projections.Snapshot(subject.SessionValue())
	if err != nil {
		t.Fatal(err)
	}
	view, found, err := subagentprojection.ReadBound(projectionSnapshot.Values)
	if err != nil {
		t.Fatal(err)
	}
	if !found || len(view.Applied) != wantCount ||
		view.Applied[len(view.Applied)-1].Revision != wantRevision {
		t.Fatalf("applied config = %#v, found = %v", view.Applied, found)
	}
}

func countSubmittedMessages(events []session.Event) int {
	count := 0
	for _, committed := range events {
		if committed.Type == agent.InboxSplicedEventName {
			count++
		}
	}
	return count
}

func followupCount(subject *boundRuntimeAgent) int {
	subject.mutex.Lock()
	defer subject.mutex.Unlock()
	return subject.followups
}

func latestBoundCursor(
	conversation session.Context,
	childID session.SessionID,
) *subagent.BoundCursor {
	var latest *subagent.BoundCursor
	for _, committed := range conversation.Events() {
		if committed.Type != subagent.BoundCursorEventName {
			continue
		}
		var cursor subagent.BoundCursor
		if json.Unmarshal(committed.Data, &cursor) != nil ||
			cursor.ChildSessionID != childID {
			continue
		}
		detached := cursor
		latest = &detached
	}
	return latest
}

func boundDelivery(
	owner *Service,
	parentID session.SessionID,
	childID session.SessionID,
) *interactionDelivery {
	owner.interactions.mutex.Lock()
	defer owner.interactions.mutex.Unlock()
	return owner.interactions.entries[parentID][childID]
}

func detachBoundDelivery(
	owner *Service,
	parentID session.SessionID,
	childID session.SessionID,
) *interactionDelivery {
	owner.interactions.mutex.Lock()
	defer owner.interactions.mutex.Unlock()
	parentDeliveries := owner.interactions.entries[parentID]
	delivery := parentDeliveries[childID]
	if parentDeliveries != nil {
		delete(parentDeliveries, childID)
		if len(parentDeliveries) == 0 {
			delete(owner.interactions.entries, parentID)
		}
	}
	return delivery
}

func waitForBoundCondition(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for Bound interaction delivery")
		}
		time.Sleep(time.Millisecond)
	}
}

func newBoundRuntimeMessage(
	t *testing.T,
	text string,
) agentmessage.UserMessage {
	t.Helper()
	messageValue, err := agentmessage.NewUserMessage(
		agentmessage.UserMessageInput{
			Content: []agentmessage.ContentBlock{
				agentmessage.NewTextBlock(text),
			},
			Source: agentmessage.UserMessageSource{
				Kind: "user",
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	return messageValue
}

func cloneInspection(source persistence.Inspection) persistence.Inspection {
	return persistence.Inspection{
		Header: source.Header,
		Events: append([]session.Event(nil), source.Events...),
	}
}

func int64Pointer(value int64) *int64 {
	return &value
}

var _ agent.Agent = (*boundRuntimeAgent)(nil)
var _ agent.Scope = (*boundRuntimeScope)(nil)
var _ agent.AgentScopeRuntime = (*boundAgentRuntime)(nil)
var _ agent.Factory = (*boundAgentFactory)(nil)
var _ FailureReporter = (*boundRuntimeFailures)(nil)
var _ Extensions = (*boundRuntimeExtensions)(nil)
