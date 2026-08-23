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
)

type agentRecord struct {
	plugin.Base
	identifier   session.SessionID
	conversation *session.Session
	options      agent.Options
	status       agent.Status
	messages     []llm.UserMessage
	injected     []llm.UserMessage
	cancel       agent.CancelOptions
	idle         chan struct{}
	idleOnce     sync.Once
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

func (subject *agentRecord) SessionValue() *session.Session { return subject.conversation }

func (*agentRecord) InboxValue() *agent.Inbox { return nil }

func (subject *agentRecord) StatusValue() agent.Status { return subject.status }

func (subject *agentRecord) Cancel(
	_ agent.CancelCause,
	options agent.CancelOptions,
) {
	subject.cancel = options
	subject.status = agent.StatusIdle
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
	task agent.MaintenanceTask,
) error {
	return task.Run(requestContext)
}

func (subject *agentRecord) Send(
	messageValue llm.UserMessage,
	_ agent.InboxTarget,
	_ bool,
) error {
	subject.messages = append(subject.messages, messageValue)
	return nil
}

func (subject *agentRecord) Followup(messageValue llm.UserMessage) error {
	subject.messages = append(subject.messages, messageValue)
	return nil
}

func (subject *agentRecord) Steer(messageValue llm.UserMessage) error {
	subject.messages = append(subject.messages, messageValue)
	return nil
}

func (subject *agentRecord) Inject(messageValue llm.UserMessage) error {
	subject.injected = append(subject.injected, messageValue)
	return nil
}

type registryRecord struct {
	plugin.Base
	mutex    sync.Mutex
	agents   map[session.SessionID]*agentRecord
	sessions *sessionRecord
	stored   *persistenceRecord
}

func (*registryRecord) RegisterFactory(
	agent.Factory,
) (agent.FactoryRegistration, error) {
	return factoryRegistrationRecord{}, nil
}

type factoryRegistrationRecord struct{}

func (factoryRegistrationRecord) Unregister() {}

func (records *registryRecord) Create(
	_ context.Context,
	options agent.CreateOptions,
) (agent.Handle, error) {
	conversation, createErr := session.New(
		options.SessionID,
		session.CreateOptions{
			Seed:     options.Seed,
			Metadata: options.Metadata,
		},
	)
	if createErr != nil {
		return agent.Handle{}, createErr
	}
	subject := &agentRecord{
		identifier:   options.SessionID,
		conversation: conversation,
		options:      options.AgentOptions,
		status:       agent.StatusRunning,
		idle:         make(chan struct{}),
	}
	records.mutex.Lock()
	if records.agents[options.SessionID] != nil {
		records.mutex.Unlock()
		return agent.Handle{}, errors.New("duplicate")
	}
	records.agents[options.SessionID] = subject
	records.sessions.entries[options.SessionID] = conversation
	records.mutex.Unlock()
	lifecycleOwner := &agentLifecycle{
		registry: records,
		subject:  subject,
	}
	return agent.NewHandle(subject, lifecycleOwner)
}

func (records *registryRecord) Resume(
	_ context.Context,
	options agent.ResumeOptions,
) (agent.Handle, error) {
	inspection, found := records.stored.inspections[options.SessionID]
	if !found {
		return agent.Handle{}, errors.New("missing inspection")
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
		return agent.Handle{}, createErr
	}
	subject := &agentRecord{
		identifier:   options.SessionID,
		conversation: conversation,
		options:      options.AgentOptions,
		status:       agent.StatusRunning,
		idle:         make(chan struct{}),
	}
	records.mutex.Lock()
	records.agents[options.SessionID] = subject
	records.sessions.entries[options.SessionID] = conversation
	records.mutex.Unlock()
	return agent.NewHandle(
		subject,
		&agentLifecycle{
			registry: records,
			subject:  subject,
		},
	)
}

func (*registryRecord) Enter(agent.Agent, agent.Agent) error { return nil }

func (*registryRecord) Announce(context.Context, agent.Agent) error { return nil }

func (records *registryRecord) Remove(
	_ context.Context,
	subject agent.Agent,
) error {
	records.mutex.Lock()
	delete(records.agents, subject.ID())
	records.mutex.Unlock()
	return nil
}

func (records *registryRecord) Get(identifier session.SessionID) (agent.Agent, bool) {
	records.mutex.Lock()
	defer records.mutex.Unlock()
	subject := records.agents[identifier]
	return subject, subject != nil
}

func (records *registryRecord) Contains(subject agent.Agent) bool {
	if subject == nil {
		return false
	}
	records.mutex.Lock()
	defer records.mutex.Unlock()
	return records.agents[subject.ID()] == subject
}

func (records *registryRecord) IsOwnedBy(
	identifier session.SessionID,
	owner agent.Agent,
) bool {
	return records.Contains(owner) && records.agents[identifier] != nil
}

func (records *registryRecord) List() []agent.Agent {
	records.mutex.Lock()
	defer records.mutex.Unlock()
	result := make([]agent.Agent, 0, len(records.agents))
	for _, subject := range records.agents {
		result = append(result, subject)
	}
	return result
}

func (records *registryRecord) Roots() []agent.Agent { return records.List() }

type agentLifecycle struct {
	registry *registryRecord
	subject  *agentRecord
}

func (lifecycleOwner *agentLifecycle) Dispose(context.Context) error {
	lifecycleOwner.registry.mutex.Lock()
	delete(lifecycleOwner.registry.agents, lifecycleOwner.subject.ID())
	delete(lifecycleOwner.registry.sessions.entries, lifecycleOwner.subject.ID())
	lifecycleOwner.registry.mutex.Unlock()
	return nil
}

type sessionRecord struct {
	plugin.Base
	entries map[session.SessionID]*session.Session
}

func (*sessionRecord) Create(
	context.Context,
	*session.SessionID,
	session.CreateOptions,
) (session.SessionHandle, error) {
	return nil, errors.New("unused")
}

func (*sessionRecord) Prepare(
	*session.SessionID,
	session.CreateOptions,
) (*session.Session, error) {
	return nil, errors.New("unused")
}

func (*sessionRecord) Enter(*session.Session) (session.SessionHandle, error) {
	return nil, errors.New("unused")
}

func (*sessionRecord) Announce(context.Context, *session.Session) error { return nil }

func (*sessionRecord) Flush(context.Context, *session.Session) error { return nil }

func (records *sessionRecord) Get(identifier session.SessionID) (*session.Session, bool) {
	conversation := records.entries[identifier]
	return conversation, conversation != nil
}

func (records *sessionRecord) List() []*session.Session {
	result := make([]*session.Session, 0, len(records.entries))
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
	started []subagent.Started
	ended   []subagent.Ended
}

func (records *lifecycleRecord) Started(_ agent.Agent, fact subagent.Started) {
	records.started = append(records.started, fact)
}

func (records *lifecycleRecord) Ended(_ agent.Agent, fact subagent.Ended) {
	records.ended = append(records.ended, fact)
}

type composerStub struct{}

func (composerStub) Compose(Composition) agent.Setup {
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
		entries: map[session.SessionID]*session.Session{
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
	lifecycleFacts := &lifecycleRecord{}
	owner, managerErr := New(Dependencies{
		Agents:      agentRegistry,
		Sessions:    liveSessions,
		Persistence: storedSessions,
		Providers: providerSource{
			candidate: providerRecord{},
		},
		Lifecycle: lifecycleFacts,
		Composer:  composerStub{},
	})
	if managerErr != nil {
		t.Fatal(managerErr)
	}
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
				ParentSession:   sessionIDPointer(parentAgent.ID()),
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
		entries: map[session.SessionID]*session.Session{
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
	lifecycleFacts := &lifecycleRecord{}
	owner, managerErr := New(Dependencies{
		Agents:      agentRegistry,
		Sessions:    liveSessions,
		Persistence: storedSessions,
		Providers: providerSource{
			candidate: providerRecord{},
		},
		Lifecycle: lifecycleFacts,
		Composer:  composerStub{},
	})
	if managerErr != nil {
		t.Fatal(managerErr)
	}
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
var _ agent.Registry = (*registryRecord)(nil)
var _ session.LiveStore = (*sessionRecord)(nil)
var _ persistence.Persistence = (*persistenceRecord)(nil)
var _ subagent.ContinuableProvider = providerRecord{}
