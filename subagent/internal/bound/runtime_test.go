package bound

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/agentmessage"
	pluginruntime "github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/session/persistence"
	sessionprojection "github.com/gorenx/goren/session/projection"
	"github.com/gorenx/goren/subagent"
	boundcontract "github.com/gorenx/goren/subagent/bound"
	sharedexecution "github.com/gorenx/goren/subagent/internal/execution"
	subagentprojection "github.com/gorenx/goren/subagent/internal/projection"
)

type boundRuntimePersistence struct {
	persistence.Persistence
	mutex sync.Mutex
	// Key is a Session ID. Value is its latest flushed durable inspection.
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

func (source *boundRuntimePersistence) store(conversation session.Context) {
	source.mutex.Lock()
	source.inspections[conversation.ID()] = persistence.Inspection{
		Header: conversation.Header(),
		Events: conversation.Events(),
	}
	source.mutex.Unlock()
}

type boundRuntimeSessions struct {
	session.LiveStore
	mutex sync.Mutex
	// Key is a live Session ID. Value is its exact current Session Context.
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

func (source *boundRuntimeSessions) enter(conversation session.Context) {
	source.mutex.Lock()
	source.entries[conversation.ID()] = conversation
	source.mutex.Unlock()
}

func (source *boundRuntimeSessions) leave(identifier session.SessionID) {
	source.mutex.Lock()
	delete(source.entries, identifier)
	source.mutex.Unlock()
}

type boundRuntimeAgent struct {
	identifier          session.SessionID
	conversation        session.Context
	options             agent.Options
	mutex               sync.Mutex
	followups           int
	idleWaits           int
	maintenanceAttempts int
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

func (subject *boundRuntimeAgent) WhenIdle(context.Context) error {
	subject.mutex.Lock()
	subject.idleWaits++
	subject.mutex.Unlock()
	return nil
}

func (subject *boundRuntimeAgent) RunMaintenance(
	requestContext context.Context,
	maintenanceAction func(context.Context) error,
) error {
	subject.mutex.Lock()
	subject.maintenanceAttempts++
	subject.mutex.Unlock()
	return maintenanceAction(requestContext)
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

func (*boundRuntimeAgent) Steer(agentmessage.UserMessage) error  { return nil }
func (*boundRuntimeAgent) Inject(agentmessage.UserMessage) error { return nil }

type boundRuntimeScope struct {
	subject agent.Agent
	mutex   sync.Mutex
	// Each value is a canonical Plugin name mounted in provisioning order.
	mounted   []string
	resources []agent.ScopeResource
}

func (scope *boundRuntimeScope) Agent() agent.Agent { return scope.subject }

func (scope *boundRuntimeScope) Own(resource agent.ScopeResource) error {
	if resource == nil {
		return errors.New("test: nil Scope resource")
	}
	scope.mutex.Lock()
	scope.resources = append(scope.resources, resource)
	scope.mutex.Unlock()
	return nil
}

func (scope *boundRuntimeScope) MountPlugin(
	_ context.Context,
	instance pluginruntime.Plugin,
) (agent.ScopeResource, error) {
	resource := &boundRuntimeResource{}
	scope.mutex.Lock()
	scope.mounted = append(scope.mounted, instance.Manifest().Name)
	scope.resources = append(scope.resources, resource)
	scope.mutex.Unlock()
	return resource, nil
}

type boundRuntimeResource struct{}

func (*boundRuntimeResource) Dispose(context.Context) error { return nil }

type boundAgentRuntime struct {
	scope    *boundRuntimeScope
	sessions *boundRuntimeSessions
}

func (*boundAgentRuntime) Dispatch(context.Context, agent.RuntimeEvent) error {
	return nil
}

func (*boundAgentRuntime) ResolvePreStep(
	requestContext context.Context,
	notice agent.PreStepNotice,
	action agent.PreStepAction,
) (agent.PreStepDecision, error) {
	return action.Execute(requestContext, notice)
}

func (*boundAgentRuntime) ResolveRequest(
	requestContext context.Context,
	notice agent.RequestNotice,
	action agent.RequestAction,
) (agent.RequestResolution, error) {
	return action.Execute(requestContext, notice)
}

func (*boundAgentRuntime) ResolveRequestError(
	requestContext context.Context,
	notice agent.RequestErrorNotice,
	handler agent.RequestErrorHandler,
) (agent.RequestErrorAction, error) {
	return handler.Execute(requestContext, notice)
}

func (runtime *boundAgentRuntime) Provision(
	requestContext context.Context,
	source agent.Provisioner,
) error {
	return agent.ApplyProvisioning(requestContext, runtime.scope, source)
}

func (runtime *boundAgentRuntime) Teardown(
	closeContext context.Context,
) error {
	runtime.scope.mutex.Lock()
	resources := append(
		[]agent.ScopeResource(nil),
		runtime.scope.resources...,
	)
	runtime.scope.resources = nil
	runtime.scope.mutex.Unlock()
	var closeErr error
	for index := len(resources) - 1; index >= 0; index-- {
		closeErr = errors.Join(
			closeErr,
			resources[index].Dispose(closeContext),
		)
	}
	runtime.sessions.leave(runtime.scope.subject.ID())
	return closeErr
}

type boundAgentFactory struct {
	sessions    *boundRuntimeSessions
	persistence *boundRuntimePersistence
	mutex       sync.Mutex
	createCalls int
	resumeCalls int
	// Key is a requested Session ID. Value is its injected construction error.
	createErrors  map[session.SessionID]error
	createEntered chan<- session.SessionID
	createRelease <-chan struct{}
	// Key is a Session ID. Value is its latest Agent epoch from this Factory.
	latestAgents map[session.SessionID]*boundRuntimeAgent
}

func (builder *boundAgentFactory) CreateAgent(
	requestContext context.Context,
	agentEpoch agent.AgentEpoch,
	settings agent.CreateOptions,
) error {
	conversation, err := session.New(
		settings.SessionID,
		session.CreateOptions{
			Seed:     settings.Seed,
			Metadata: settings.Metadata,
		},
	)
	if err != nil {
		return err
	}
	builder.mutex.Lock()
	builder.createCalls++
	createErr := builder.createErrors[settings.SessionID]
	createEntered := builder.createEntered
	createRelease := builder.createRelease
	builder.mutex.Unlock()
	if createErr != nil {
		return createErr
	}
	if createEntered != nil {
		select {
		case createEntered <- settings.SessionID:
		case <-requestContext.Done():
			return context.Cause(requestContext)
		}
	}
	if createRelease != nil {
		select {
		case <-createRelease:
		case <-requestContext.Done():
			return context.Cause(requestContext)
		}
	}
	return builder.attach(
		requestContext,
		agentEpoch,
		conversation,
		settings.AgentOptions,
		settings.Provisioner,
	)
}

func (builder *boundAgentFactory) ResumeAgent(
	requestContext context.Context,
	agentEpoch agent.AgentEpoch,
	settings agent.ResumeOptions,
) error {
	inspection, err := builder.persistence.Inspect(
		requestContext,
		settings.SessionID,
	)
	if err != nil {
		return err
	}
	conversation, err := session.New(
		settings.SessionID,
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
		requestContext,
		agentEpoch,
		conversation,
		settings.AgentOptions,
		settings.Provisioner,
	)
}

func (builder *boundAgentFactory) attach(
	requestContext context.Context,
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
	scope := &boundRuntimeScope{subject: subject}
	if source != nil {
		if err := agent.ApplyProvisioning(
			requestContext,
			scope,
			source,
		); err != nil {
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
	reconciliations  []ReconcileFailure
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

func (reporter *boundRuntimeFailures) ReportBoundReconcileFailure(
	failure ReconcileFailure,
) {
	reporter.mutex.Lock()
	reporter.reconciliations = append(reporter.reconciliations, failure)
	reporter.mutex.Unlock()
}

type boundRuntimeExtensions struct {
	mutex        sync.Mutex
	provisionErr error
}

func (selection *boundRuntimeExtensions) Provision(
	[]string,
) (agent.Provisioner, error) {
	selection.mutex.Lock()
	defer selection.mutex.Unlock()
	return nil, selection.provisionErr
}

type boundRuntimeFixture struct {
	owner        *Service
	agents       *agent.RegistryService
	agentFactory *boundAgentFactory
	extensions   *boundRuntimeExtensions
	failures     *boundRuntimeFailures
	parent       agent.Handle
	projections  *sessionprojection.DriveRegistry
}

func newBoundRuntimeFixture(
	testingContext *testing.T,
	definitions ...boundcontract.Definition,
) boundRuntimeFixture {
	testingContext.Helper()
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
		testingContext.Fatal(err)
	}
	testingContext.Cleanup(registration.Close)
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
		testingContext.Fatal(err)
	}
	projections := sessionprojection.NewDriveRegistry()
	for _, unit := range subagentprojection.Units() {
		if _, err = projections.Register(unit); err != nil {
			testingContext.Fatal(err)
		}
	}
	failures := &boundRuntimeFailures{}
	definitionPersistence := newDefinitionStoreStub(testingContext)
	definitionPersistence.load = append(
		[]boundcontract.Definition(nil),
		definitions...,
	)
	extensionSelection := &boundRuntimeExtensions{}
	owner, err := New(
		context.Background(),
		Dependencies{
			Agents:      agentRegistry,
			Constructor: agentRegistry,
			Sessions:    sessions,
			Persistence: persistenceSource,
			Projections: projections,
			Definitions: definitionPersistence,
			Extensions:  extensionSelection,
			Executions:  sharedexecution.NewRegistry(),
			Failures:    failures,
		},
	)
	if err != nil {
		testingContext.Fatal(err)
	}
	testingContext.Cleanup(func() {
		if closeErr := owner.Close(context.Background()); closeErr != nil {
			testingContext.Error(closeErr)
		}
		if closeErr := parentHandle.Dispose(context.Background()); closeErr != nil {
			testingContext.Error(closeErr)
		}
	})
	return boundRuntimeFixture{
		owner:        owner,
		agents:       agentRegistry,
		agentFactory: agentFactory,
		extensions:   extensionSelection,
		failures:     failures,
		parent:       parentHandle,
		projections:  projections,
	}
}

func TestDefinitionCommitDoesNotRequireSelectedExtensionToBeLive(
	testingContext *testing.T,
) {
	fixture := newBoundRuntimeFixture(testingContext)
	sentinel := errors.New("test: selected Extension is unavailable")
	fixture.extensions.mutex.Lock()
	fixture.extensions.provisionErr = sentinel
	fixture.extensions.mutex.Unlock()
	created, err := fixture.owner.Create(
		context.Background(),
		boundcontract.Creation{
			Definition: boundcontract.Draft{
				Name:         "researcher",
				Enabled:      true,
				SystemPrompt: "prompt",
				Extensions:   []string{"temporarily-missing"},
			},
		},
	)
	if err != nil {
		testingContext.Fatal(err)
	}
	if created.Revision != 1 {
		testingContext.Fatalf("created Definition = %#v", created)
	}
	listed, err := fixture.owner.List(context.Background())
	if err != nil {
		testingContext.Fatal(err)
	}
	if len(listed) != 1 || listed[0].Name != "researcher" {
		testingContext.Fatalf("committed Definitions = %#v", listed)
	}
	waitForBoundCondition(testingContext, func() bool {
		fixture.failures.mutex.Lock()
		defer fixture.failures.mutex.Unlock()
		return len(fixture.failures.materializations) == 1 &&
			errors.Is(
				fixture.failures.materializations[0].Error,
				sentinel,
			)
	})
}

func TestSessionStartedBindsAndMaterializesDefinitionsConcurrently(
	testingContext *testing.T,
) {
	fixture := newBoundRuntimeFixture(
		testingContext,
		runtimeDefinition(testingContext, "first", 1, true, "first prompt"),
		runtimeDefinition(testingContext, "second", 1, true, "second prompt"),
	)
	entered := make(chan session.SessionID, 2)
	release := make(chan struct{})
	var releaseOnce sync.Once
	testingContext.Cleanup(func() {
		releaseOnce.Do(func() { close(release) })
	})
	fixture.agentFactory.mutex.Lock()
	fixture.agentFactory.createEntered = entered
	fixture.agentFactory.createRelease = release
	fixture.agentFactory.mutex.Unlock()
	fixture.owner.SessionStarted(fixture.parent.Subject)
	// Key is a generated child Session ID. Value records entry before release.
	observed := make(map[session.SessionID]bool, 2)
	for len(observed) != 2 {
		select {
		case childID := <-entered:
			observed[childID] = true
		case <-time.After(2 * time.Second):
			testingContext.Fatal(
				"different Bound children did not materialize concurrently",
			)
		}
	}
	releaseOnce.Do(func() { close(release) })
	waitForBoundCondition(testingContext, func() bool {
		view := runtimeBoundView(testingContext, fixture)
		return len(view.Bindings) == 2 && len(view.Materializations) == 2
	})
	view := runtimeBoundView(testingContext, fixture)
	if view.Bindings[0].Name != "first" || view.Bindings[1].Name != "second" {
		testingContext.Fatalf("Bindings = %#v", view.Bindings)
	}
	if view.Bindings[0].Seq+1 != view.Bindings[1].Seq {
		testingContext.Fatalf(
			"Bindings were not committed in one contiguous batch: %#v",
			view.Bindings,
		)
	}
}

func TestOneDefinitionBindsIndependentlyToMultipleUserSessions(
	testingContext *testing.T,
) {
	fixture := newBoundRuntimeFixture(
		testingContext,
		runtimeDefinition(testingContext, "researcher", 1, true, "prompt"),
	)
	secondParent, err := fixture.agents.Create(
		context.Background(),
		agent.CreateOptions{
			SessionID: "parent-2",
			AgentOptions: agent.Options{
				Provider: "provider",
				Model:    "model",
			},
		},
	)
	if err != nil {
		testingContext.Fatal(err)
	}
	testingContext.Cleanup(func() {
		if disposeErr := secondParent.Dispose(context.Background()); disposeErr != nil {
			testingContext.Error(disposeErr)
		}
	})
	fixture.owner.SessionStarted(fixture.parent.Subject)
	fixture.owner.SessionStarted(secondParent.Subject)
	var firstBinding subagentprojection.BoundBinding
	var secondBinding subagentprojection.BoundBinding
	waitForBoundCondition(testingContext, func() bool {
		firstView := runtimeBoundViewForAgent(
			testingContext,
			fixture.projections,
			fixture.parent.Subject,
		)
		secondView := runtimeBoundViewForAgent(
			testingContext,
			fixture.projections,
			secondParent.Subject,
		)
		var firstFound bool
		var secondFound bool
		firstBinding, firstFound = firstView.BindingNamed("researcher")
		secondBinding, secondFound = secondView.BindingNamed("researcher")
		return firstFound && secondFound &&
			len(firstView.Materializations) == 1 &&
			len(secondView.Materializations) == 1
	})
	if firstBinding.ChildSessionID == secondBinding.ChildSessionID {
		testingContext.Fatal("different user Sessions shared one Bound child")
	}
}

func TestBoundChildSessionDoesNotRecursivelyCreateBindings(
	testingContext *testing.T,
) {
	fixture := newBoundRuntimeFixture(
		testingContext,
		runtimeDefinition(testingContext, "researcher", 1, true, "prompt"),
	)
	fixture.owner.SessionStarted(fixture.parent.Subject)
	bindingValue := waitForRuntimeBinding(testingContext, fixture, "researcher")
	waitForRuntimeMaterialization(testingContext, fixture, "researcher", 1)
	childAgent, found := fixture.agents.Get(bindingValue.ChildSessionID)
	if !found {
		testingContext.Fatal("Bound child is not resident")
	}
	fixture.agentFactory.mutex.Lock()
	createCalls := fixture.agentFactory.createCalls
	fixture.agentFactory.mutex.Unlock()
	fixture.owner.SessionStarted(childAgent)
	childView := runtimeBoundViewForAgent(
		testingContext,
		fixture.projections,
		childAgent,
	)
	if len(childView.Bindings) != 0 {
		testingContext.Fatalf(
			"Bound child recursively acquired Bindings: %#v",
			childView.Bindings,
		)
	}
	fixture.agentFactory.mutex.Lock()
	createCallsAfter := fixture.agentFactory.createCalls
	fixture.agentFactory.mutex.Unlock()
	if createCallsAfter != createCalls {
		testingContext.Fatalf(
			"child SessionStarted created %d extra Agents",
			createCallsAfter-createCalls,
		)
	}
}

func TestReconcileUsesSessionFIFOWithoutParentMaintenance(
	testingContext *testing.T,
) {
	fixture := newBoundRuntimeFixture(
		testingContext,
		runtimeDefinition(testingContext, "researcher", 1, true, "prompt"),
	)
	parentAgent := fixture.parent.Subject.(*boundRuntimeAgent)
	fixture.owner.SessionStarted(parentAgent)
	waitForRuntimeBinding(testingContext, fixture, "researcher")
	parentAgent.mutex.Lock()
	idleWaits := parentAgent.idleWaits
	maintenanceAttempts := parentAgent.maintenanceAttempts
	parentAgent.mutex.Unlock()
	if idleWaits != 0 || maintenanceAttempts != 0 {
		testingContext.Fatalf(
			"idle waits = %d, maintenance attempts = %d; want 0 and 0",
			idleWaits,
			maintenanceAttempts,
		)
	}
}

func TestCloseCancelsBlockedBoundActivation(
	testingContext *testing.T,
) {
	fixture := newBoundRuntimeFixture(
		testingContext,
		runtimeDefinition(testingContext, "researcher", 1, true, "prompt"),
	)
	entered := make(chan session.SessionID, 1)
	release := make(chan struct{})
	fixture.agentFactory.mutex.Lock()
	fixture.agentFactory.createEntered = entered
	fixture.agentFactory.createRelease = release
	fixture.agentFactory.mutex.Unlock()
	fixture.owner.SessionStarted(fixture.parent.Subject)
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		testingContext.Fatal("Bound activation did not reach child construction")
	}
	closed := make(chan error, 1)
	go func() {
		closed <- fixture.owner.Close(context.Background())
	}()
	select {
	case err := <-closed:
		if err != nil {
			testingContext.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		close(release)
		testingContext.Fatal("Bound Close did not cancel blocked activation")
	}
	close(release)
}

func TestBindingFreezesCompletedPrefixAndCreatesIdleChild(
	testingContext *testing.T,
) {
	fixture := newBoundRuntimeFixture(
		testingContext,
		runtimeDefinition(
			testingContext,
			"researcher",
			1,
			true,
			"Research in the background.",
		),
	)
	parentSession := fixture.parent.Subject.SessionValue()
	appendTurnStart(testingContext, parentSession, 1)
	appendInteractionUser(
		testingContext,
		parentSession,
		agentmessage.UserMessageSource{},
		"historical question",
	)
	appendTurnEnd(testingContext, parentSession, 1, session.TurnCompleted{})
	frozenPrefix := parentSession.Events()
	fixture.owner.SessionStarted(fixture.parent.Subject)
	bindingValue := waitForRuntimeBinding(testingContext, fixture, "researcher")
	waitForRuntimeMaterialization(testingContext, fixture, "researcher", 1)
	if bindingValue.ContextNextSeq != int64(len(frozenPrefix)) {
		testingContext.Fatalf(
			"ContextNextSeq = %d, want %d",
			bindingValue.ContextNextSeq,
			len(frozenPrefix),
		)
	}
	childAgent, found := fixture.agents.Get(bindingValue.ChildSessionID)
	if !found {
		testingContext.Fatal("Bound child is not resident")
	}
	childSession := childAgent.SessionValue()
	if childSession.Header().SeedLength == nil ||
		*childSession.Header().SeedLength != int64(len(frozenPrefix)) {
		testingContext.Fatalf("child Header = %#v", childSession.Header())
	}
	childEvents := childSession.Events()
	if len(childEvents) < len(frozenPrefix) ||
		!reflect.DeepEqual(childEvents[:len(frozenPrefix)], frozenPrefix) {
		testingContext.Fatalf(
			"child seed = %#v, want exact prefix %#v",
			childEvents,
			frozenPrefix,
		)
	}
	childRecord := fixture.agentFactory.latestAgents[bindingValue.ChildSessionID]
	if followupCount(childRecord) != 0 ||
		countInboxSplicesAfterSeed(childSession) != 0 {
		testingContext.Fatal("fresh Bound child was started with an Inbox prompt")
	}
	assertAppliedRevision(
		testingContext,
		fixture.projections,
		childAgent,
		1,
		1,
	)
}

func TestDefinitionReplacementReusesSessionAndPreservesDisabledBacklog(
	testingContext *testing.T,
) {
	fixture := newBoundRuntimeFixture(
		testingContext,
		runtimeDefinition(testingContext, "researcher", 1, true, "revision one"),
	)
	fixture.owner.SessionStarted(fixture.parent.Subject)
	bindingValue := waitForRuntimeBinding(testingContext, fixture, "researcher")
	waitForRuntimeMaterialization(testingContext, fixture, "researcher", 1)
	firstAgent, found := fixture.agents.Get(bindingValue.ChildSessionID)
	if !found {
		testingContext.Fatal("first Bound epoch is missing")
	}
	if _, err := fixture.owner.Replace(
		context.Background(),
		boundcontract.Replacement{
			ExpectedRevision: 1,
			Definition: boundcontract.Draft{
				Name:         "researcher",
				Enabled:      false,
				SystemPrompt: "revision two",
			},
		},
	); err != nil {
		testingContext.Fatal(err)
	}
	waitForBoundCondition(testingContext, func() bool {
		_, resident := fixture.agents.Get(bindingValue.ChildSessionID)
		return !resident
	})
	parentSession := fixture.parent.Subject.SessionValue()
	appendTurnStart(testingContext, parentSession, 1)
	appendInteractionUser(
		testingContext,
		parentSession,
		agentmessage.UserMessageSource{},
		"queued while disabled",
	)
	appendTurnEnd(testingContext, parentSession, 1, session.TurnCompleted{})
	worker := fixture.owner.workers.find(
		fixture.parent.Subject.ID(),
		bindingValue.ChildSessionID,
	)
	if worker == nil {
		testingContext.Fatal("disabled Binding lost its worker")
	}
	if err := worker.catchUp(context.Background()); err != nil {
		testingContext.Fatal(err)
	}
	if latestBoundCursor(
		parentSession,
		"researcher",
		bindingValue.ChildSessionID,
	) != nil {
		testingContext.Fatal("disabled Bound advanced its interaction cursor")
	}
	if _, err := fixture.owner.Replace(
		context.Background(),
		boundcontract.Replacement{
			ExpectedRevision: 2,
			Definition: boundcontract.Draft{
				Name:         "researcher",
				Enabled:      true,
				SystemPrompt: "revision three",
			},
		},
	); err != nil {
		testingContext.Fatal(err)
	}
	waitForRuntimeMaterialization(testingContext, fixture, "researcher", 3)
	waitForBoundCondition(testingContext, func() bool {
		return latestBoundCursor(
			parentSession,
			"researcher",
			bindingValue.ChildSessionID,
		) != nil
	})
	secondAgent, found := fixture.agents.Get(bindingValue.ChildSessionID)
	if !found || agent.Same(firstAgent, secondAgent) {
		testingContext.Fatal("Definition replacement did not publish a new epoch")
	}
	if secondAgent.ID() != firstAgent.ID() {
		testingContext.Fatal("Definition replacement changed the child Session")
	}
	fixture.agentFactory.mutex.Lock()
	resumeCalls := fixture.agentFactory.resumeCalls
	fixture.agentFactory.mutex.Unlock()
	if resumeCalls != 1 {
		testingContext.Fatalf("cold resume calls = %d, want 1", resumeCalls)
	}
	if followupCount(
		fixture.agentFactory.latestAgents[bindingValue.ChildSessionID],
	) != 1 {
		testingContext.Fatal("re-enabled Bound did not consume backlog exactly once")
	}
	assertAppliedRevision(
		testingContext,
		fixture.projections,
		secondAgent,
		3,
		2,
	)
}

func TestParentInteractionDeliveryIsIdempotentAndRepairsCursorFromReceipt(
	testingContext *testing.T,
) {
	fixture := newBoundRuntimeFixture(
		testingContext,
		runtimeDefinition(testingContext, "researcher", 1, true, "prompt"),
	)
	fixture.owner.SessionStarted(fixture.parent.Subject)
	bindingValue := waitForRuntimeBinding(testingContext, fixture, "researcher")
	waitForRuntimeMaterialization(testingContext, fixture, "researcher", 1)
	parentSession := fixture.parent.Subject.SessionValue()
	parentAgent := fixture.parent.Subject.(*boundRuntimeAgent)
	parentAgent.mutex.Lock()
	maintenanceAttempts := parentAgent.maintenanceAttempts
	parentAgent.mutex.Unlock()
	appendTurnStart(testingContext, parentSession, 1)
	appendInteractionUser(
		testingContext,
		parentSession,
		agentmessage.UserMessageSource{},
		"investigate this",
	)
	appendInteractionAssistant(
		testingContext,
		parentSession,
		1,
		1,
		[]agentmessage.ContentBlock{
			agentmessage.NewTextBlock("parent response"),
		},
	)
	appendTurnEnd(testingContext, parentSession, 1, session.TurnCompleted{})
	ended := parentSession.Events()[parentSession.Seq()-1]
	fixture.owner.SessionEventAppended(session.EventAppended{
		Conversation: parentSession,
		Committed:    ended,
	})
	waitForBoundCondition(testingContext, func() bool {
		return latestBoundCursor(
			parentSession,
			"researcher",
			bindingValue.ChildSessionID,
		) != nil
	})
	parentAgent.mutex.Lock()
	maintenanceAttemptsAfterDelivery := parentAgent.maintenanceAttempts
	parentAgent.mutex.Unlock()
	if maintenanceAttemptsAfterDelivery != maintenanceAttempts {
		testingContext.Fatalf(
			"turn/end started %d unnecessary parent maintenance attempts",
			maintenanceAttemptsAfterDelivery-maintenanceAttempts,
		)
	}
	childAgent := fixture.agentFactory.latestAgents[bindingValue.ChildSessionID]
	if followupCount(childAgent) != 1 {
		testingContext.Fatalf(
			"child followups = %d, want one interaction",
			followupCount(childAgent),
		)
	}
	worker := fixture.owner.workers.find(
		fixture.parent.Subject.ID(),
		bindingValue.ChildSessionID,
	)
	if worker == nil {
		testingContext.Fatal("Bound worker is missing")
	}
	if err := worker.catchUp(context.Background()); err != nil {
		testingContext.Fatal(err)
	}
	if followupCount(childAgent) != 1 {
		testingContext.Fatal("cursor catch-up duplicated delivered interaction")
	}
	appendTurnStart(testingContext, parentSession, 2)
	appendInteractionUser(
		testingContext,
		parentSession,
		agentmessage.UserMessageSource{},
		"repair this",
	)
	appendTurnEnd(testingContext, parentSession, 2, session.TurnBlocked{})
	nextSeq, err := boundCursor(
		parentSession.Events(),
		"researcher",
		bindingValue.ChildSessionID,
		bindingValue.Seq+1,
	)
	if err != nil {
		testingContext.Fatal(err)
	}
	interaction, found, err := nextParentInteraction(
		parentSession.Events(),
		nextSeq,
	)
	if err != nil || !found || !interaction.deliverable {
		testingContext.Fatalf(
			"repair interaction = %#v, found = %v, error = %v",
			interaction,
			found,
			err,
		)
	}
	source := subagent.Delivery{
		ParentSessionID: fixture.parent.Subject.ID(),
		Turn:            interaction.turn,
		FromSeq:         interaction.fromSeq,
		ThroughSeq:      interaction.nextSeq - 1,
		Outcome:         interaction.outcome,
	}
	messageValue, err := agentmessage.NewUserMessage(
		agentmessage.UserMessageInput{
			Content: interaction.content,
			Source:  source,
		},
	)
	if err != nil {
		testingContext.Fatal(err)
	}
	if err = childAgent.Followup(messageValue); err != nil {
		testingContext.Fatal(err)
	}
	if err = fixture.owner.dependencies.Sessions.Flush(
		context.Background(),
		childAgent.SessionValue(),
	); err != nil {
		testingContext.Fatal(err)
	}
	if err = worker.catchUp(context.Background()); err != nil {
		testingContext.Fatal(err)
	}
	if followupCount(childAgent) != 2 {
		testingContext.Fatal("receipt recovery redelivered the parent interaction")
	}
	cursor := latestBoundCursor(
		parentSession,
		"researcher",
		bindingValue.ChildSessionID,
	)
	if cursor == nil || cursor.ThroughTurn != 2 ||
		cursor.Disposition != boundcontract.CursorDelivered {
		testingContext.Fatalf("repaired Cursor = %#v", cursor)
	}
}

func TestMaterializationFailureIsContainedAndReported(
	testingContext *testing.T,
) {
	fixture := newBoundRuntimeFixture(
		testingContext,
		runtimeDefinition(testingContext, "researcher", 1, true, "prompt"),
	)
	bindings, err := fixture.owner.ensureBindings(
		context.Background(),
		fixture.parent.Subject,
	)
	if err != nil || len(bindings) != 1 {
		testingContext.Fatalf("ensure Bindings = %#v, error = %v", bindings, err)
	}
	sentinel := errors.New("test: child create failed")
	fixture.agentFactory.mutex.Lock()
	fixture.agentFactory.createErrors[bindings[0].ChildSessionID] = sentinel
	fixture.agentFactory.mutex.Unlock()
	fixture.owner.SessionStarted(fixture.parent.Subject)
	waitForBoundCondition(testingContext, func() bool {
		fixture.failures.mutex.Lock()
		defer fixture.failures.mutex.Unlock()
		return len(fixture.failures.materializations) == 1 &&
			len(fixture.failures.reconciliations) == 1
	})
	if !fixture.agents.Contains(fixture.parent.Subject) {
		testingContext.Fatal("child failure removed the parent Agent")
	}
	view := runtimeBoundView(testingContext, fixture)
	if len(view.Materializations) != 1 ||
		view.Materializations[0].Result !=
			boundcontract.MaterializationFailed {
		testingContext.Fatalf("materialization view = %#v", view)
	}
	fixture.failures.mutex.Lock()
	materializationRecord := fixture.failures.materializations[0]
	reconciliationRecord := fixture.failures.reconciliations[0]
	fixture.failures.mutex.Unlock()
	if !errors.Is(materializationRecord.Error, sentinel) ||
		!errors.Is(reconciliationRecord.Error, sentinel) {
		testingContext.Fatalf(
			"failures = materialization:%v reconcile:%v",
			materializationRecord.Error,
			reconciliationRecord.Error,
		)
	}
	fixture.agentFactory.mutex.Lock()
	delete(fixture.agentFactory.createErrors, bindings[0].ChildSessionID)
	fixture.agentFactory.mutex.Unlock()
	fixture.owner.SessionStarted(fixture.parent.Subject)
	waitForRuntimeMaterialization(testingContext, fixture, "researcher", 1)
}

func TestBoundExecutionSettlesWhenParentCloses(
	testingContext *testing.T,
) {
	fixture := newBoundRuntimeFixture(
		testingContext,
		runtimeDefinition(testingContext, "researcher", 1, true, "prompt"),
	)
	fixture.owner.SessionStarted(fixture.parent.Subject)
	bindingValue := waitForRuntimeBinding(testingContext, fixture, "researcher")
	waitForRuntimeMaterialization(testingContext, fixture, "researcher", 1)
	worker := fixture.owner.workers.find(
		fixture.parent.Subject.ID(),
		bindingValue.ChildSessionID,
	)
	if worker == nil || worker.current == nil {
		testingContext.Fatal("Bound execution is missing")
	}
	running := worker.current.execution
	if err := fixture.parent.Dispose(context.Background()); err != nil {
		testingContext.Fatal(err)
	}
	waitContext, cancelWait := context.WithTimeout(
		context.Background(),
		2*time.Second,
	)
	defer cancelWait()
	if err := running.Wait(waitContext); err != nil {
		testingContext.Fatal(err)
	}
	if running.State() != subagent.ExecutionStopped {
		testingContext.Fatalf(
			"Execution state = %s, want stopped",
			running.State(),
		)
	}
}

func runtimeDefinition(
	testingContext *testing.T,
	definitionName string,
	revision int64,
	isEnabled bool,
	systemPrompt string,
) boundcontract.Definition {
	testingContext.Helper()
	definitionValue, err := boundcontract.NewDefinition(
		boundcontract.Draft{
			Name:         definitionName,
			Enabled:      isEnabled,
			SystemPrompt: systemPrompt,
		},
		revision,
	)
	if err != nil {
		testingContext.Fatal(err)
	}
	return definitionValue
}

func runtimeBoundView(
	testingContext *testing.T,
	fixture boundRuntimeFixture,
) subagentprojection.Bound {
	testingContext.Helper()
	return runtimeBoundViewForAgent(
		testingContext,
		fixture.projections,
		fixture.parent.Subject,
	)
}

func runtimeBoundViewForAgent(
	testingContext *testing.T,
	projections *sessionprojection.DriveRegistry,
	subject agent.Agent,
) subagentprojection.Bound {
	testingContext.Helper()
	view, err := readBoundProjection(
		projections,
		subject.SessionValue(),
	)
	if err != nil {
		testingContext.Fatal(err)
	}
	return view
}

func waitForRuntimeBinding(
	testingContext *testing.T,
	fixture boundRuntimeFixture,
	definitionName string,
) subagentprojection.BoundBinding {
	testingContext.Helper()
	var bindingValue subagentprojection.BoundBinding
	waitForBoundCondition(testingContext, func() bool {
		view := runtimeBoundView(testingContext, fixture)
		var found bool
		bindingValue, found = view.BindingNamed(definitionName)
		return found
	})
	return bindingValue
}

func waitForRuntimeMaterialization(
	testingContext *testing.T,
	fixture boundRuntimeFixture,
	definitionName string,
	revision int64,
) {
	testingContext.Helper()
	waitForBoundCondition(testingContext, func() bool {
		view := runtimeBoundView(testingContext, fixture)
		for _, materialization := range view.Materializations {
			if materialization.Name == definitionName &&
				materialization.DefinitionRevision == revision &&
				materialization.Result ==
					boundcontract.MaterializationSucceeded {
				return true
			}
		}
		return false
	})
}

func assertAppliedRevision(
	testingContext *testing.T,
	projections *sessionprojection.DriveRegistry,
	subject agent.Agent,
	wantRevision int64,
	wantCount int,
) {
	testingContext.Helper()
	projectionSnapshot, err := projections.Snapshot(subject.SessionValue())
	if err != nil {
		testingContext.Fatal(err)
	}
	view, found, err := subagentprojection.ReadBound(projectionSnapshot.Values)
	if err != nil {
		testingContext.Fatal(err)
	}
	if !found || len(view.Applied) != wantCount ||
		view.Applied[len(view.Applied)-1].Definition.Revision != wantRevision {
		testingContext.Fatalf("applied Definitions = %#v", view.Applied)
	}
}

func countInboxSplicesAfterSeed(conversation session.Context) int {
	startSeq := int64(0)
	if seedLength := conversation.Header().SeedLength; seedLength != nil {
		startSeq = *seedLength
	}
	count := 0
	for _, committed := range conversation.Events() {
		if committed.Seq >= startSeq &&
			committed.Type == agent.InboxSplicedEventName {
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
	definitionName string,
	childID session.SessionID,
) *boundcontract.Cursor {
	var latest *boundcontract.Cursor
	for _, committed := range conversation.Events() {
		if committed.Type != boundcontract.CursorEventName {
			continue
		}
		var cursor boundcontract.Cursor
		if json.Unmarshal(committed.Data, &cursor) != nil ||
			cursor.Name != definitionName ||
			cursor.ChildSessionID != childID {
			continue
		}
		detached := cursor
		latest = &detached
	}
	return latest
}

func waitForBoundCondition(
	testingContext *testing.T,
	condition func() bool,
) {
	testingContext.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			testingContext.Fatal("timed out waiting for Bound condition")
		}
		time.Sleep(time.Millisecond)
	}
}

func cloneInspection(source persistence.Inspection) persistence.Inspection {
	return persistence.Inspection{
		Header: source.Header,
		Events: append([]session.Event(nil), source.Events...),
	}
}

func int64Pointer(value int64) *int64 { return &value }

var _ agent.Agent = (*boundRuntimeAgent)(nil)
var _ agent.Scope = (*boundRuntimeScope)(nil)
var _ agent.AgentScopeRuntime = (*boundAgentRuntime)(nil)
var _ agent.Factory = (*boundAgentFactory)(nil)
var _ FailureReporter = (*boundRuntimeFailures)(nil)
var _ Extensions = (*boundRuntimeExtensions)(nil)
