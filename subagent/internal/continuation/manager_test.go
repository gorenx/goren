package continuation

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/session/persistence"
	"github.com/gorenx/goren/subagent"
	"github.com/gorenx/goren/subagent/internal/childscope"
)

type agentRecord struct {
	plugin.Base
	mutex        sync.Mutex
	identifier   session.SessionID
	conversation session.Context
	options      agent.Options
	status       agent.Status
	messages     []llm.UserMessage
	injected     []llm.UserMessage
	followupErr  error
	cancel       agent.CancelOptions
	idle         chan struct{}
	idleOnce     sync.Once
	statusRead   chan<- struct{}
	statusResume <-chan struct{}
}

func (subject *agentRecord) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: "continuation-test-agent:" + string(subject.identifier),
	}
}

func (*agentRecord) Apply(context.Context) error { return nil }

func (*agentRecord) Dispose(context.Context) error { return nil }

func (subject *agentRecord) ID() session.SessionID { return subject.identifier }

func (subject *agentRecord) OptionsValue() agent.Options { return subject.options }

func (subject *agentRecord) SessionValue() session.Context { return subject.conversation }

func (*agentRecord) InboxValue() *agent.Inbox { return nil }

func (subject *agentRecord) StatusValue() agent.Status {
	subject.mutex.Lock()
	currentStatus := subject.status
	statusRead := subject.statusRead
	statusResume := subject.statusResume
	subject.mutex.Unlock()
	if statusRead != nil {
		select {
		case statusRead <- struct{}{}:
		default:
		}
	}
	if statusResume != nil {
		<-statusResume
	}
	return currentStatus
}

func (subject *agentRecord) Cancel(
	_ agent.CancelCause,
	options agent.CancelOptions,
) {
	subject.mutex.Lock()
	subject.cancel = options
	subject.status = agent.StatusIdle
	subject.mutex.Unlock()
	subject.idleOnce.Do(func() {
		close(subject.idle)
	})
}

func (subject *agentRecord) WhenIdle(requestContext context.Context) error {
	select {
	case <-requestContext.Done():
		return requestContext.Err()
	case <-subject.idle:
		return nil
	}
}

func (*agentRecord) RunMaintenance(
	requestContext context.Context,
	operation func(context.Context) error,
) error {
	return operation(requestContext)
}

func (subject *agentRecord) Send(
	messageValue llm.UserMessage,
	_ agent.InboxTarget,
	_ bool,
) error {
	subject.mutex.Lock()
	defer subject.mutex.Unlock()
	subject.messages = append(subject.messages, messageValue)
	return nil
}

func (subject *agentRecord) Followup(messageValue llm.UserMessage) error {
	subject.mutex.Lock()
	defer subject.mutex.Unlock()
	if subject.followupErr != nil {
		return subject.followupErr
	}
	subject.messages = append(subject.messages, messageValue)
	return nil
}

func (subject *agentRecord) Steer(messageValue llm.UserMessage) error {
	subject.mutex.Lock()
	defer subject.mutex.Unlock()
	subject.messages = append(subject.messages, messageValue)
	return nil
}

func (subject *agentRecord) Inject(messageValue llm.UserMessage) error {
	subject.mutex.Lock()
	defer subject.mutex.Unlock()
	subject.injected = append(subject.injected, messageValue)
	return nil
}

func (subject *agentRecord) becomeIdle() {
	subject.mutex.Lock()
	subject.status = agent.StatusIdle
	subject.mutex.Unlock()
	subject.idleOnce.Do(func() {
		close(subject.idle)
	})
}

func (subject *agentRecord) pauseStatusRead(
	observed chan<- struct{},
	resumeSignal <-chan struct{},
) {
	subject.mutex.Lock()
	subject.statusRead = observed
	subject.statusResume = resumeSignal
	subject.mutex.Unlock()
}
func (subject *agentRecord) messagesSnapshot() []llm.UserMessage {
	subject.mutex.Lock()
	defer subject.mutex.Unlock()
	return append([]llm.UserMessage(nil), subject.messages...)
}

type registryRecord struct {
	mutex       sync.Mutex
	agents      map[session.SessionID]*agentRecord
	sessions    *sessionRecord
	stored      *persistenceRecord
	disposeErr  error
	followupErr error
	service     *agent.RegistryService
	closing     map[session.SessionID]<-chan struct{}
	disposed    func(agent.Agent)
}

func (records *registryRecord) start(t *testing.T) {
	t.Helper()
	records.service = agent.NewRegistry(agent.RegistryOptions{})
	if _, err := records.service.RegisterFactory(records); err != nil {
		t.Fatal(err)
	}
	existing := make([]*agentRecord, 0, len(records.agents))
	for _, subject := range records.agents {
		existing = append(existing, subject)
	}
	for _, subject := range existing {
		if _, err := records.service.Create(
			context.Background(),
			agent.CreateOptions{
				SessionID:    subject.ID(),
				AgentOptions: subject.OptionsValue(),
			},
		); err != nil {
			t.Fatal(err)
		}
	}
}

func (records *registryRecord) CreateAgent(
	requestContext context.Context,
	agentEpoch agent.AgentEpoch,
	options agent.CreateOptions,
) error {
	records.mutex.Lock()
	subject := records.agents[options.SessionID]
	records.mutex.Unlock()
	if subject != nil {
		return records.attach(requestContext, agentEpoch, subject, options.Provisioner)
	}
	conversation, createErr := session.New(
		options.SessionID,
		session.CreateOptions{
			Seed:     options.Seed,
			Metadata: options.Metadata,
		},
	)
	if createErr != nil {
		return createErr
	}
	subject = &agentRecord{
		identifier:   options.SessionID,
		conversation: conversation,
		options:      options.AgentOptions,
		status:       agent.StatusRunning,
		idle:         make(chan struct{}),
		followupErr:  records.followupErr,
	}
	records.mutex.Lock()
	if records.agents[options.SessionID] != nil {
		records.mutex.Unlock()
		return errors.New("duplicate")
	}
	records.agents[options.SessionID] = subject
	records.sessions.entries[options.SessionID] = conversation
	records.mutex.Unlock()
	return records.attach(requestContext, agentEpoch, subject, options.Provisioner)
}

func (records *registryRecord) ResumeAgent(
	requestContext context.Context,
	agentEpoch agent.AgentEpoch,
	options agent.ResumeOptions,
) error {
	inspection, found := records.stored.inspections[options.SessionID]
	if !found {
		return errors.New("missing inspection")
	}
	conversation, createErr := session.New(
		options.SessionID,
		session.CreateOptions{
			Seed: inspection.Events,
			Metadata: session.Metadata{
				CreatedAt:       &inspection.Header.CreatedAt,
				CWD:             inspection.Header.CWD,
				ParentSession:   inspection.Header.ParentSession,
				SeedLength:      inspection.Header.SeedLength,
				Origin:          inspection.Header.Origin,
				DelegationDepth: inspection.Header.DelegationDepth,
				AgentPreset:     inspection.Header.AgentPreset,
			},
		},
	)
	if createErr != nil {
		return createErr
	}
	subject := &agentRecord{
		identifier:   options.SessionID,
		conversation: conversation,
		options:      options.AgentOptions,
		status:       agent.StatusRunning,
		idle:         make(chan struct{}),
		followupErr:  records.followupErr,
	}
	records.mutex.Lock()
	records.agents[options.SessionID] = subject
	records.sessions.entries[options.SessionID] = conversation
	records.mutex.Unlock()
	return records.attach(requestContext, agentEpoch, subject, options.Provisioner)
}

func (records *registryRecord) attach(
	requestContext context.Context,
	agentEpoch agent.AgentEpoch,
	subject *agentRecord,
	scopeProvisioner agent.Provisioner,
) error {
	records.mutex.Lock()
	if records.closing == nil {
		records.closing = make(map[session.SessionID]<-chan struct{})
	}
	records.closing[subject.ID()] = agentEpoch.ClosingSignal()
	records.mutex.Unlock()
	runtime := &registryScopeRuntime{
		owner:   records,
		subject: subject,
	}
	if _, err := agentEpoch.Attach(subject, runtime); err != nil {
		return err
	}
	if scopeProvisioner == nil {
		return nil
	}
	return runtime.Provision(requestContext, scopeProvisioner)
}

func (records *registryRecord) closingSignal(
	identifier session.SessionID,
) <-chan struct{} {
	records.mutex.Lock()
	signal := records.closing[identifier]
	records.mutex.Unlock()
	return signal
}

func (records *registryRecord) Get(identifier session.SessionID) (agent.Agent, bool) {
	return records.service.Get(identifier)
}

func (records *registryRecord) Contains(subject agent.Agent) bool {
	return records.service.Contains(subject)
}

func (records *registryRecord) List() []agent.Agent {
	return records.service.List()
}

func (records *registryRecord) HasRuntimeDescendants(parentAgent agent.Agent) bool {
	return records.service.HasRuntimeDescendants(parentAgent)
}

func (records *registryRecord) CloseDescendants(
	closeContext context.Context,
	parentAgent agent.Agent,
) error {
	return records.service.CloseDescendants(closeContext, parentAgent)
}

type registryScopeRuntime struct {
	mutex     sync.Mutex
	owner     *registryRecord
	subject   *agentRecord
	resources []agent.ScopeResource
}

func (runtime *registryScopeRuntime) Dispatch(
	_ context.Context,
	fact agent.RuntimeEvent,
) error {
	if notice, matches := fact.(agent.Disposed); matches &&
		runtime.owner.disposed != nil {
		runtime.owner.disposed(notice.Subject)
	}
	return nil
}

func (*registryScopeRuntime) ResolvePreStep(
	requestContext context.Context,
	notice agent.PreStepNotice,
	terminal agent.PreStepAction,
) (agent.PreStepDecision, error) {
	return terminal.Execute(requestContext, notice)
}

func (*registryScopeRuntime) ResolveRequest(
	requestContext context.Context,
	notice agent.RequestNotice,
	terminal agent.RequestAction,
) (agent.RequestResolution, error) {
	return terminal.Execute(requestContext, notice)
}

func (*registryScopeRuntime) ResolveRequestError(
	requestContext context.Context,
	notice agent.RequestErrorNotice,
	terminal agent.RequestErrorHandler,
) (agent.RequestErrorAction, error) {
	return terminal.Execute(requestContext, notice)
}

func (runtime *registryScopeRuntime) Agent() agent.Agent {
	return runtime.subject
}

func (runtime *registryScopeRuntime) Own(resource agent.ScopeResource) error {
	runtime.mutex.Lock()
	runtime.resources = append(runtime.resources, resource)
	runtime.mutex.Unlock()
	return nil
}

func (runtime *registryScopeRuntime) Provision(
	requestContext context.Context,
	scopeProvisioner agent.Provisioner,
) error {
	return agent.ApplyProvisioning(requestContext, runtime, scopeProvisioner)
}

func (runtime *registryScopeRuntime) Teardown(closeContext context.Context) error {
	runtime.mutex.Lock()
	resources := append([]agent.ScopeResource(nil), runtime.resources...)
	runtime.resources = nil
	runtime.mutex.Unlock()
	var closeErr error
	for index := len(resources) - 1; index >= 0; index-- {
		closeErr = errors.Join(closeErr, resources[index].Dispose(closeContext))
	}
	runtime.owner.mutex.Lock()
	delete(runtime.owner.agents, runtime.subject.ID())
	delete(runtime.owner.sessions.entries, runtime.subject.ID())
	delete(runtime.owner.closing, runtime.subject.ID())
	disposeErr := runtime.owner.disposeErr
	runtime.owner.mutex.Unlock()
	return errors.Join(closeErr, disposeErr)
}

var _ agent.Factory = (*registryRecord)(nil)
var _ agent.AgentScopeRuntime = (*registryScopeRuntime)(nil)
var _ agent.Scope = (*registryScopeRuntime)(nil)

type sessionRecord struct {
	plugin.Base
	entries  map[session.SessionID]session.Context
	flushErr error
	stored   *persistenceRecord
}

func (*sessionRecord) Create(
	context.Context,
	*session.SessionID,
	session.CreateOptions,
) (session.Handle, error) {
	return nil, errors.New("unused")
}

func (*sessionRecord) Prepare(
	*session.SessionID,
	session.CreateOptions,
) (session.Context, error) {
	return nil, errors.New("unused")
}

func (*sessionRecord) Enter(session.Context) (session.Handle, error) {
	return nil, errors.New("unused")
}

func (*sessionRecord) Announce(context.Context, session.Context) error { return nil }

func (records *sessionRecord) Flush(
	_ context.Context,
	conversation session.Context,
) error {
	if records.flushErr != nil {
		return records.flushErr
	}
	if records.stored != nil {
		records.stored.inspections[conversation.ID()] = persistence.Inspection{
			Header: conversation.Header(),
			Events: conversation.Events(),
		}
	}
	return nil
}

func (records *sessionRecord) Get(identifier session.SessionID) (session.Context, bool) {
	conversation := records.entries[identifier]
	return conversation, conversation != nil
}

func (records *sessionRecord) List() []session.Context {
	result := make([]session.Context, 0, len(records.entries))
	for _, conversation := range records.entries {
		result = append(result, conversation)
	}
	return result
}

type persistenceRecord struct {
	plugin.Base
	snapshots   []persistence.Snapshot
	inspections map[session.SessionID]persistence.Inspection
}

func (*persistenceRecord) Locate(session.Header) (persistence.Location, bool) {
	return persistence.Location{}, false
}

func (*persistenceRecord) SupportsRawArtifacts() bool { return false }

func (*persistenceRecord) ReadRaw(
	context.Context,
	session.SessionID,
) (persistence.RawArtifact, bool, error) {
	return persistence.RawArtifact{}, false, nil
}

func (*persistenceRecord) Create(context.Context, session.Header) error { return nil }

func (*persistenceRecord) Append(
	context.Context,
	session.SessionID,
	[]session.Event,
) error {
	return nil
}

func (*persistenceRecord) Prepare(
	context.Context,
	session.SessionID,
) (*session.Preparation, error) {
	return nil, errors.New("unused")
}

func (*persistenceRecord) Load(
	context.Context,
	session.SessionID,
) (persistence.Inspection, error) {
	return persistence.Inspection{}, errors.New("unused")
}

func (records *persistenceRecord) Inspect(
	_ context.Context,
	identifier session.SessionID,
) (persistence.Inspection, error) {
	inspection, found := records.inspections[identifier]
	if !found {
		return persistence.Inspection{}, errors.New("missing inspection")
	}
	return inspection, nil
}

func (*persistenceRecord) ReadFrom(
	context.Context,
	session.SessionID,
	int64,
) (persistence.Inspection, error) {
	return persistence.Inspection{}, errors.New("unused")
}

func (*persistenceRecord) List(context.Context) ([]session.Header, error) {
	return nil, nil
}

func (records *persistenceRecord) ListSnapshots(
	context.Context,
) ([]persistence.Snapshot, error) {
	return append([]persistence.Snapshot(nil), records.snapshots...), nil
}

type providerRecord struct{}

func (providerRecord) Name() string { return "spawn" }

func (providerRecord) Capabilities() subagent.Capabilities {
	return subagent.Capabilities{}
}

func (providerRecord) InheritsParentContext() bool { return false }

func (providerRecord) Start(
	context.Context,
	subagent.ResolvedStartRequest,
) (subagent.Run, error) {
	return nil, errors.New("unused")
}

func (providerRecord) PrepareContinuable(
	context.Context,
	subagent.ContinuableCreateRequest,
) (subagent.ContinuableCreateSpec, error) {
	return subagent.ContinuableCreateSpec{}, nil
}

type providerSource struct {
	candidate subagent.Provider
}

func (records providerSource) GetProvider(providerName string) (subagent.Provider, bool) {
	return records.candidate, providerName == records.candidate.Name()
}

type lifecycleRecord struct {
	mutex       sync.Mutex
	started     []subagent.Started
	ended       []subagent.Ended
	startedHook func(agent.Agent, subagent.Started)
	endedHook   func(agent.Agent, subagent.Ended)
}

type failureRecord struct {
	mutex    sync.Mutex
	failures []FinalFlushFailure
}

func (records *failureRecord) ReportFinalFlushFailure(
	failure FinalFlushFailure,
) {
	records.mutex.Lock()
	records.failures = append(records.failures, failure)
	records.mutex.Unlock()
}

func (records *failureRecord) failuresSnapshot() []FinalFlushFailure {
	records.mutex.Lock()
	defer records.mutex.Unlock()
	return append([]FinalFlushFailure(nil), records.failures...)
}

func (records *lifecycleRecord) Started(parentAgent agent.Agent, fact subagent.Started) {
	records.mutex.Lock()
	records.started = append(records.started, fact)
	hook := records.startedHook
	records.mutex.Unlock()
	if hook != nil {
		hook(parentAgent, fact)
	}
}

func (records *lifecycleRecord) Ended(parentAgent agent.Agent, fact subagent.Ended) {
	records.mutex.Lock()
	records.ended = append(records.ended, fact)
	hook := records.endedHook
	records.mutex.Unlock()
	if hook != nil {
		hook(parentAgent, fact)
	}
}

func (records *lifecycleRecord) startedSnapshot() []subagent.Started {
	records.mutex.Lock()
	defer records.mutex.Unlock()
	return append([]subagent.Started(nil), records.started...)
}

func (records *lifecycleRecord) endedSnapshot() []subagent.Ended {
	records.mutex.Lock()
	defer records.mutex.Unlock()
	return append([]subagent.Ended(nil), records.ended...)
}

type scopeBuilderStub struct{}

func (scopeBuilderStub) Provisioner(childscope.ContinuableInput) agent.Provisioner {
	return nil
}

func TestContinuableFreshLifecycleAndControl(t *testing.T) {
	parentSession, sessionErr := session.New("parent", session.CreateOptions{})
	if sessionErr != nil {
		t.Fatal(sessionErr)
	}
	parentAgent := &agentRecord{
		identifier:   "parent",
		conversation: parentSession,
		options: agent.Options{
			Provider: "deepseek",
			Model:    "chat",
		},
		status: agent.StatusRunning,
		idle:   make(chan struct{}),
	}
	liveSessions := &sessionRecord{
		entries: map[session.SessionID]session.Context{
			"parent": parentSession,
		},
	}
	storedSessions := &persistenceRecord{
		inspections: make(map[session.SessionID]persistence.Inspection),
	}
	agentRegistry := &registryRecord{
		agents: map[session.SessionID]*agentRecord{
			"parent": parentAgent,
		},
		sessions: liveSessions,
		stored:   storedSessions,
	}
	agentRegistry.start(t)
	lifecycleFacts := &lifecycleRecord{}
	owner, managerErr := New(Dependencies{
		Agents:      agentRegistry.service,
		Constructor: agentRegistry.service,
		Descendants: agentRegistry.service,
		Sessions:    liveSessions,
		Persistence: storedSessions,
		Providers: providerSource{
			candidate: providerRecord{},
		},
		Lifecycle: lifecycleFacts,
		Scopes:    scopeBuilderStub{},
		Failures:  &failureRecord{},
	})
	if managerErr != nil {
		t.Fatal(managerErr)
	}
	agentRegistry.disposed = owner.AgentDisposed
	childID := session.SessionID("child")
	startResult, startErr := owner.Start(
		context.Background(),
		subagent.ContinuableStartSpec{
			Provider: "spawn",
			Label:    "review",
			ChildID:  &childID,
			Request: subagent.ContinuableRequest{
				Prompt: []llm.ContentBlock{
					llm.NewTextBlock("inspect"),
				},
				Parent: parentAgent,
			},
		},
	)
	if startErr != nil {
		t.Fatal(startErr)
	}
	if startResult.ChildID != childID || startResult.MessageID == "" {
		t.Fatalf("start identities = %#v", startResult)
	}
	childAgent := agentRegistry.agents[childID]
	if childAgent == nil || len(childAgent.messages) != 1 {
		t.Fatal("initial prompt was not accepted by child Inbox")
	}
	identity, found, foldErr := subagent.FoldDescriptor(
		childAgent.SessionValue().Events(),
	)
	if foldErr != nil || !found {
		t.Fatalf("descriptor fold = %#v, %v, %v", identity, found, foldErr)
	}
	continuableIdentity := identity.(subagent.ContinuableDescriptor)
	if continuableIdentity.Provider != "spawn" ||
		continuableIdentity.AgentProvider == nil ||
		*continuableIdentity.AgentProvider != "deepseek" {
		t.Fatalf("descriptor = %#v", continuableIdentity)
	}
	if len(lifecycleFacts.started) != 1 || lifecycleFacts.started[0].ID != childID {
		t.Fatalf("started facts = %#v", lifecycleFacts.started)
	}

	messageID, followErr := owner.Followup(
		context.Background(),
		parentAgent,
		childID,
		[]llm.ContentBlock{
			llm.NewTextBlock("continue"),
		},
		subagent.FollowupOptions{
			Source: subagent.CoordinatorSource{
				SenderSessionID: parentAgent.ID(),
			},
		},
	)
	if followErr != nil || messageID == "" || len(childAgent.messages) != 2 {
		t.Fatalf("followup = %q, %v, messages=%d", messageID, followErr, len(childAgent.messages))
	}
	if interruptErr := owner.Interrupt(
		childID,
		subagent.UserInterruptAuthority{
			ParentSessionID: parentAgent.ID(),
		},
	); interruptErr != nil {
		t.Fatal(interruptErr)
	}
	if !childAgent.cancel.KeepInbox {
		t.Fatal("interrupt did not preserve unclaimed Inbox work")
	}
	if _, reportErr := owner.ReportFrom(
		context.Background(),
		childAgent,
		[]llm.ContentBlock{
			llm.NewTextBlock("result"),
		},
		subagent.ReportOptions{
			Delivery: subagent.ReportQuiet,
		},
	); reportErr != nil {
		t.Fatal(reportErr)
	}
	if len(parentAgent.injected) != 1 ||
		parentAgent.injected[0].SourceValue().SourceKind() != "subagent-report" {
		t.Fatal("report was not quietly attributed to the child")
	}
	if drainErr := owner.DrainChildren(
		context.Background(),
		parentAgent,
		[]session.SessionID{childID},
	); drainErr != nil {
		t.Fatal(drainErr)
	}
	if _, live := agentRegistry.Get(childID); live {
		t.Fatal("drained child remained live")
	}
	if len(lifecycleFacts.ended) != 1 ||
		lifecycleFacts.ended[0].RunID != lifecycleFacts.started[0].RunID {
		t.Fatalf("ended facts = %#v", lifecycleFacts.ended)
	}
	if drainErr := owner.DrainDescendants(
		context.Background(),
		[]agent.Agent{parentAgent},
	); drainErr != nil {
		t.Fatal(drainErr)
	}
	_, restartErr := owner.Start(
		context.Background(),
		subagent.ContinuableStartSpec{
			Provider: "spawn",
			Label:    "late",
			Request: subagent.ContinuableRequest{
				Parent: parentAgent,
			},
		},
	)
	var problem *subagent.Error
	if !errors.As(restartErr, &problem) || problem.Code != subagent.ErrorDraining {
		t.Fatalf("Start below drained parent = %v", restartErr)
	}
}

func TestFollowupColdResumesPersistedContinuableChild(t *testing.T) {
	parentSession, sessionErr := session.New("parent", session.CreateOptions{})
	if sessionErr != nil {
		t.Fatal(sessionErr)
	}
	parentAgent := &agentRecord{
		identifier:   "parent",
		conversation: parentSession,
		options: agent.Options{
			Provider: "deepseek",
			Model:    "chat",
		},
		status: agent.StatusRunning,
		idle:   make(chan struct{}),
	}
	childID := session.SessionID("persisted-child")
	identity := subagent.ContinuableDescriptor{
		Provider:      "spawn",
		Label:         "resume",
		AgentProvider: stringPointer("deepseek"),
		AgentModel:    stringPointer("chat"),
	}
	seed, seedErr := descriptorSeed(childID, nil, identity)
	if seedErr != nil {
		t.Fatal(seedErr)
	}
	depth := int64(1)
	persistedSession, createErr := session.New(
		childID,
		session.CreateOptions{
			Seed: seed,
			Metadata: session.Metadata{
				ParentSession:   sessionIDReference(parentAgent.ID()),
				Origin:          session.OriginSubagent,
				DelegationDepth: &depth,
			},
		},
	)
	if createErr != nil {
		t.Fatal(createErr)
	}
	storedSessions := &persistenceRecord{
		inspections: map[session.SessionID]persistence.Inspection{
			childID: {
				Header: persistedSession.Header(),
				Events: persistedSession.Events(),
			},
		},
	}
	liveSessions := &sessionRecord{
		entries: map[session.SessionID]session.Context{
			"parent": parentSession,
		},
	}
	agentRegistry := &registryRecord{
		agents: map[session.SessionID]*agentRecord{
			"parent": parentAgent,
		},
		sessions: liveSessions,
		stored:   storedSessions,
	}
	agentRegistry.start(t)
	lifecycleFacts := &lifecycleRecord{}
	owner, managerErr := New(Dependencies{
		Agents:      agentRegistry.service,
		Constructor: agentRegistry.service,
		Descendants: agentRegistry.service,
		Sessions:    liveSessions,
		Persistence: storedSessions,
		Providers: providerSource{
			candidate: providerRecord{},
		},
		Lifecycle: lifecycleFacts,
		Scopes:    scopeBuilderStub{},
		Failures:  &failureRecord{},
	})
	if managerErr != nil {
		t.Fatal(managerErr)
	}
	agentRegistry.disposed = owner.AgentDisposed
	messageID, followErr := owner.Followup(
		context.Background(),
		parentAgent,
		childID,
		[]llm.ContentBlock{
			llm.NewTextBlock("resume work"),
		},
		subagent.FollowupOptions{
			Source: subagent.CoordinatorSource{
				SenderSessionID: parentAgent.ID(),
			},
		},
	)
	if followErr != nil || messageID == "" {
		t.Fatalf("cold Followup = %q, %v", messageID, followErr)
	}
	resumedAgent := agentRegistry.agents[childID]
	if resumedAgent == nil || len(resumedAgent.messages) != 1 {
		t.Fatal("cold resume did not accept the waiting turn")
	}
	if len(lifecycleFacts.started) != 1 || lifecycleFacts.started[0].ID != childID {
		t.Fatalf("cold resume lifecycle = %#v", lifecycleFacts.started)
	}
	if drainErr := owner.DrainChildren(
		context.Background(),
		parentAgent,
		[]session.SessionID{childID},
	); drainErr != nil {
		t.Fatal(drainErr)
	}
}

var _ agent.Agent = (*agentRecord)(nil)

func sessionIDReference(value session.SessionID) *session.SessionID {
	return &value
}

var _ agent.Registry = (*registryRecord)(nil)
var _ session.LiveStore = (*sessionRecord)(nil)
var _ persistence.Persistence = (*persistenceRecord)(nil)
var _ subagent.ContinuableProvider = providerRecord{}
