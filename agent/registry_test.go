package agent_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	agentcore "github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/session"
)

type fakeAgent struct {
	plugin.Base
	identifier   session.SessionID
	conversation session.Context
	status       agentcore.Status
	runtime      agentcore.AgentScopeRuntime
}

func (subject *fakeAgent) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: "test-agent:" + string(subject.identifier),
		Provides: []plugin.ProvidedService{
			plugin.NewProvidedService[agentcore.Agent](subject),
		},
	}
}

func (*fakeAgent) Apply(context.Context) error   { return nil }
func (*fakeAgent) Dispose(context.Context) error { return nil }
func (subject *fakeAgent) ID() session.SessionID { return subject.identifier }
func (subject *fakeAgent) ScopeRuntimeValue() agentcore.AgentScopeRuntime {
	return subject.runtime
}
func (*fakeAgent) OptionsValue() agentcore.Options {
	return agentcore.Options{}
}
func (subject *fakeAgent) SessionValue() session.Context { return subject.conversation }
func (*fakeAgent) InboxValue() *agentcore.Inbox          { return nil }
func (subject *fakeAgent) StatusValue() agentcore.Status { return subject.status }
func (*fakeAgent) Cancel(agentcore.CancelCause, agentcore.CancelOptions) {
}
func (*fakeAgent) WhenIdle(context.Context) error { return nil }
func (*fakeAgent) RunMaintenance(
	requestContext context.Context,
	operation func(context.Context) error,
) error {
	return operation(requestContext)
}
func (*fakeAgent) Send(llm.UserMessage, agentcore.InboxTarget, bool) error {
	return nil
}
func (*fakeAgent) Followup(llm.UserMessage) error { return nil }
func (*fakeAgent) Steer(llm.UserMessage) error    { return nil }
func (*fakeAgent) Inject(llm.UserMessage) error   { return nil }

func newFakeAgent(t *testing.T, identifier session.SessionID) *fakeAgent {
	t.Helper()
	conversation, err := session.New(identifier, session.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return &fakeAgent{
		identifier:   identifier,
		conversation: conversation,
		status:       agentcore.StatusIdle,
	}
}

type fakeScopeRuntime struct {
	source   plugin.Plugin
	events   []agentcore.RuntimeEvent
	closed   *[]session.SessionID
	subject  agentcore.Agent
	closeErr error
}

func (runtime *fakeScopeRuntime) Dispatch(
	requestContext context.Context,
	fact agentcore.RuntimeEvent,
) error {
	runtime.events = append(runtime.events, fact)
	if runtime.source == nil {
		return nil
	}
	runtimeFact, matches := fact.(plugin.Event)
	if !matches {
		return errors.New("test: RuntimeEvent has no Plugin metadata")
	}
	return plugin.PublishEvent(requestContext, runtime.source, runtimeFact)
}

func (runtime *fakeScopeRuntime) ResolvePreStep(
	requestContext context.Context,
	notice agentcore.PreStepNotice,
	terminal agentcore.PreStepAction,
) (agentcore.PreStepDecision, error) {
	return plugin.Run(requestContext, runtime.source, notice, terminal)
}

func (runtime *fakeScopeRuntime) ResolveRequest(
	requestContext context.Context,
	notice agentcore.RequestNotice,
	terminal agentcore.RequestAction,
) (agentcore.RequestResolution, error) {
	return plugin.Run(requestContext, runtime.source, notice, terminal)
}

func (runtime *fakeScopeRuntime) ResolveRequestError(
	requestContext context.Context,
	notice agentcore.RequestErrorNotice,
	terminal agentcore.RequestErrorHandler,
) (agentcore.RequestErrorAction, error) {
	return plugin.Run(requestContext, runtime.source, notice, terminal)
}

func (*fakeScopeRuntime) Provision(context.Context, agentcore.Provisioner) error {
	return nil
}

func (runtime *fakeScopeRuntime) Teardown(context.Context) error {
	if runtime.closed != nil && runtime.subject != nil {
		*runtime.closed = append(*runtime.closed, runtime.subject.ID())
	}
	return runtime.closeErr
}

func startRegistry(
	t *testing.T,
	plugins ...plugin.Plugin,
) (*plugin.Runtime, *agentcore.RegistryService, plugin.Handle) {
	t.Helper()
	registry := agentcore.NewRegistry(agentcore.RegistryOptions{})
	adapter, err := agentcore.NewRegistryPlugin(registry)
	if err != nil {
		t.Fatal(err)
	}
	instances := append([]plugin.Plugin{adapter}, plugins...)
	runtimeEngine := plugin.NewRuntime(plugin.RuntimeSettings{})
	handles, err := runtimeEngine.Start(context.Background(), instances...)
	if err != nil {
		t.Fatal(err)
	}
	return runtimeEngine, registry, handles[0]
}

func mountFakeAgent(
	t *testing.T,
	runtimeEngine *plugin.Runtime,
	parent plugin.Handle,
	identifier session.SessionID,
) (*fakeAgent, plugin.Handle) {
	t.Helper()
	subject := newFakeAgent(t, identifier)
	runtime := &fakeScopeRuntime{
		source: subject,
	}
	subject.runtime = runtime
	handle, err := runtimeEngine.MountScopedChild(
		context.Background(),
		parent,
		subject,
	)
	if err != nil {
		t.Fatal(err)
	}
	return subject, handle
}

type factoryRecord struct {
	createCalls int
	createErr   error
	closed      []session.SessionID
	closeErrors map[session.SessionID]error
}

type blockingFactory struct {
	entered chan struct{}
	closing chan struct{}
	release chan struct{}
}

type gatedScopeRuntime struct {
	*fakeScopeRuntime
	entered chan struct{}
	release chan struct{}
	once    sync.Once
	calls   atomic.Int32
}

func (runtime *gatedScopeRuntime) Teardown(context.Context) error {
	runtime.calls.Add(1)
	runtime.once.Do(func() {
		close(runtime.entered)
	})
	<-runtime.release
	return runtime.fakeScopeRuntime.Teardown(context.Background())
}

type gatedFactory struct {
	runtime *gatedScopeRuntime
}

func (builder *gatedFactory) CreateAgent(
	_ context.Context,
	agentEpoch agentcore.AgentEpoch,
	options agentcore.CreateOptions,
) error {
	subject := newFakeAgentForFactory(options.SessionID)
	builder.runtime.fakeScopeRuntime.subject = subject
	subject.runtime = builder.runtime
	_, err := agentEpoch.Attach(subject, builder.runtime)
	return err
}

func (builder *gatedFactory) ResumeAgent(
	requestContext context.Context,
	agentEpoch agentcore.AgentEpoch,
	options agentcore.ResumeOptions,
) error {
	return builder.CreateAgent(
		requestContext,
		agentEpoch,
		agentcore.CreateOptions{
			SessionID: options.SessionID,
		},
	)
}

type joiningFactory struct {
	childEntered chan struct{}
	childClosing chan struct{}
	childRelease chan struct{}
	closed       []session.SessionID
}

func (builder *joiningFactory) CreateAgent(
	_ context.Context,
	agentEpoch agentcore.AgentEpoch,
	options agentcore.CreateOptions,
) error {
	if options.RuntimeParent != nil {
		close(builder.childEntered)
		<-agentEpoch.ClosingSignal()
		close(builder.childClosing)
		<-builder.childRelease
		return errors.New("test: child construction rolled back")
	}
	subject := newFakeAgentForFactory(options.SessionID)
	runtime := &fakeScopeRuntime{
		closed:  &builder.closed,
		subject: subject,
	}
	subject.runtime = runtime
	_, err := agentEpoch.Attach(subject, runtime)
	return err
}

func (builder *joiningFactory) ResumeAgent(
	requestContext context.Context,
	agentEpoch agentcore.AgentEpoch,
	options agentcore.ResumeOptions,
) error {
	return builder.CreateAgent(
		requestContext,
		agentEpoch,
		agentcore.CreateOptions{
			SessionID:     options.SessionID,
			RuntimeParent: options.RuntimeParent,
		},
	)
}

type publicationRejectingFactory struct {
	registry *agentcore.RegistryService
	closed   []session.SessionID
	child    agentcore.Handle
}

type publicationRejectingRuntime struct {
	*fakeScopeRuntime
	factory *publicationRejectingFactory
}

func (runtime *publicationRejectingRuntime) Dispatch(
	requestContext context.Context,
	fact agentcore.RuntimeEvent,
) error {
	if err := runtime.fakeScopeRuntime.Dispatch(requestContext, fact); err != nil {
		return err
	}
	created, matches := fact.(agentcore.Created)
	if !matches || created.Subject.ID() != "publishing-parent" {
		return nil
	}
	child, err := runtime.factory.registry.Create(
		requestContext,
		agentcore.CreateOptions{
			SessionID:     "publishing-child",
			RuntimeParent: created.Subject,
		},
	)
	if err != nil {
		return err
	}
	runtime.factory.child = child
	return errors.New("test: parent publication rejected")
}

func (builder *publicationRejectingFactory) CreateAgent(
	_ context.Context,
	agentEpoch agentcore.AgentEpoch,
	options agentcore.CreateOptions,
) error {
	subject := newFakeAgentForFactory(options.SessionID)
	baseRuntime := &fakeScopeRuntime{
		closed:  &builder.closed,
		subject: subject,
	}
	runtime := &publicationRejectingRuntime{
		fakeScopeRuntime: baseRuntime,
		factory:          builder,
	}
	subject.runtime = runtime
	_, err := agentEpoch.Attach(subject, runtime)
	return err
}

func (builder *publicationRejectingFactory) ResumeAgent(
	requestContext context.Context,
	agentEpoch agentcore.AgentEpoch,
	options agentcore.ResumeOptions,
) error {
	return builder.CreateAgent(
		requestContext,
		agentEpoch,
		agentcore.CreateOptions{
			SessionID:     options.SessionID,
			RuntimeParent: options.RuntimeParent,
		},
	)
}

func (builder *blockingFactory) CreateAgent(
	_ context.Context,
	agentEpoch agentcore.AgentEpoch,
	_ agentcore.CreateOptions,
) error {
	close(builder.entered)
	<-agentEpoch.ClosingSignal()
	if builder.closing != nil {
		close(builder.closing)
	}
	if builder.release != nil {
		<-builder.release
	}
	return errors.New("test: construction closed")
}

func (builder *blockingFactory) ResumeAgent(
	requestContext context.Context,
	agentEpoch agentcore.AgentEpoch,
	options agentcore.ResumeOptions,
) error {
	return builder.CreateAgent(
		requestContext,
		agentEpoch,
		agentcore.CreateOptions{
			SessionID: options.SessionID,
		},
	)
}

func (builder *factoryRecord) CreateAgent(
	_ context.Context,
	agentEpoch agentcore.AgentEpoch,
	options agentcore.CreateOptions,
) error {
	builder.createCalls++
	if builder.createErr != nil {
		return builder.createErr
	}
	return builder.attach(agentEpoch, options.SessionID)
}

func (builder *factoryRecord) ResumeAgent(
	_ context.Context,
	agentEpoch agentcore.AgentEpoch,
	options agentcore.ResumeOptions,
) error {
	return builder.attach(agentEpoch, options.SessionID)
}

func (builder *factoryRecord) attach(
	agentEpoch agentcore.AgentEpoch,
	identifier session.SessionID,
) error {
	subject := newFakeAgentForFactory(identifier)
	runtime := &fakeScopeRuntime{
		closed:   &builder.closed,
		subject:  subject,
		closeErr: builder.closeErrors[identifier],
	}
	subject.runtime = runtime
	_, err := agentEpoch.Attach(subject, runtime)
	return err
}

func newFakeAgentForFactory(identifier session.SessionID) *fakeAgent {
	conversation, _ := session.New(identifier, session.CreateOptions{})
	return &fakeAgent{
		identifier:   identifier,
		conversation: conversation,
		status:       agentcore.StatusIdle,
	}
}

func TestRegistryFactoryRegistrationOwnsExactEntry(t *testing.T) {
	t.Parallel()
	registry := agentcore.NewRegistry(agentcore.RegistryOptions{})
	first := &factoryRecord{}
	registration, err := registry.RegisterFactory(first)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = registry.RegisterFactory(&factoryRecord{}); err == nil {
		t.Fatal("Registry accepted a second Factory")
	}
	registration.Close()
	registration.Close()
	if _, err = registry.Create(
		context.Background(),
		agentcore.CreateOptions{
			SessionID: "after-factory-close",
		},
	); err == nil || !strings.Contains(err.Error(), "no Agent factory") {
		t.Fatalf("post-close Create error = %v", err)
	}
	replacement := &factoryRecord{}
	replacementRegistration, err := registry.RegisterFactory(replacement)
	if err != nil {
		t.Fatalf("register replacement Factory: %v", err)
	}
	replacementHandle, err := registry.Create(
		context.Background(),
		agentcore.CreateOptions{
			SessionID: "replacement-factory-agent",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err = replacementHandle.Dispose(context.Background()); err != nil {
		t.Fatal(err)
	}
	replacementRegistration.Close()
}

func TestFactoryRegistrationClosePreservesExistingAgents(t *testing.T) {
	t.Parallel()
	registry := agentcore.NewRegistry(agentcore.RegistryOptions{})
	builder := &factoryRecord{}
	registration, err := registry.RegisterFactory(builder)
	if err != nil {
		t.Fatal(err)
	}
	handleState, err := registry.Create(
		context.Background(),
		agentcore.CreateOptions{
			SessionID: "live-before-factory-close",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	registration.Close()
	if retained, found := registry.Get(handleState.Subject.ID()); !found ||
		!agentcore.Same(retained, handleState.Subject) {
		t.Fatal("Factory registration Close removed an existing Agent")
	}
	if err = handleState.Dispose(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestFactoryRegistrationCloseCancelsConstruction(t *testing.T) {
	t.Parallel()
	registry := agentcore.NewRegistry(agentcore.RegistryOptions{})
	builder := &blockingFactory{
		entered: make(chan struct{}),
	}
	registration, err := registry.RegisterFactory(builder)
	if err != nil {
		t.Fatal(err)
	}
	createDone := make(chan error, 1)
	go func() {
		_, createErr := registry.Create(
			context.Background(),
			agentcore.CreateOptions{
				SessionID: "closing-construction",
			},
		)
		createDone <- createErr
	}()
	<-builder.entered
	registration.Close()
	select {
	case err = <-createDone:
		if err == nil || !strings.Contains(err.Error(), "construction closed") {
			t.Fatalf("Create error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Factory registration Close did not cancel construction")
	}
	if err = registry.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestFactoryRegistrationCloseDoesNotCancelReplacementConstruction(
	t *testing.T,
) {
	t.Parallel()
	registry := agentcore.NewRegistry(agentcore.RegistryOptions{})
	oldFactory := &blockingFactory{
		entered: make(chan struct{}),
		closing: make(chan struct{}),
		release: make(chan struct{}),
	}
	oldRegistration, err := registry.RegisterFactory(oldFactory)
	if err != nil {
		t.Fatal(err)
	}
	oldCreateDone := make(chan error, 1)
	go func() {
		_, createErr := registry.Create(
			context.Background(),
			agentcore.CreateOptions{
				SessionID: "old-factory-construction",
			},
		)
		oldCreateDone <- createErr
	}()
	<-oldFactory.entered
	oldCloseDone := make(chan struct{})
	go func() {
		oldRegistration.Close()
		close(oldCloseDone)
	}()
	<-oldFactory.closing

	replacementFactory := &factoryRecord{}
	replacementRegistration, err := registry.RegisterFactory(replacementFactory)
	if err != nil {
		t.Fatalf("register replacement Factory: %v", err)
	}
	replacement, err := registry.Create(
		context.Background(),
		agentcore.CreateOptions{
			SessionID: "replacement-construction",
		},
	)
	if err != nil {
		t.Fatalf("replacement Create: %v", err)
	}
	select {
	case <-oldCloseDone:
		t.Fatal("old registration Close returned before its construction joined")
	default:
	}
	close(oldFactory.release)
	if err = <-oldCreateDone; err == nil ||
		!strings.Contains(err.Error(), "construction closed") {
		t.Fatalf("old Create error = %v", err)
	}
	<-oldCloseDone
	if retained, found := registry.Get(replacement.Subject.ID()); !found ||
		!agentcore.Same(retained, replacement.Subject) {
		t.Fatal("old registration Close affected the replacement Agent")
	}
	if err = replacement.Dispose(context.Background()); err != nil {
		t.Fatal(err)
	}
	replacementRegistration.Close()
}

func TestRegistryOwnsRuntimeParentAndClosesChildFirst(t *testing.T) {
	t.Parallel()
	registry := agentcore.NewRegistry(agentcore.RegistryOptions{})
	builder := &factoryRecord{}
	registration, err := registry.RegisterFactory(builder)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(registration.Close)
	root, err := registry.Create(
		context.Background(),
		agentcore.CreateOptions{
			SessionID: "root",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	child, err := registry.Create(
		context.Background(),
		agentcore.CreateOptions{
			SessionID:     "child",
			RuntimeParent: root.Subject,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !registry.HasRuntimeDescendants(root.Subject) {
		t.Fatal("Registry lost the exact runtime child")
	}
	if err = root.Dispose(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := []session.SessionID{child.Subject.ID(), root.Subject.ID()}
	if !reflect.DeepEqual(builder.closed, want) {
		t.Fatalf("close order = %#v, want %#v", builder.closed, want)
	}
	if _, found := registry.Get(root.Subject.ID()); found {
		t.Fatal("closed root remained live")
	}
}

func TestParentClosingBeforeChildReserveDoesNotInvokeFactory(t *testing.T) {
	t.Parallel()
	registry := agentcore.NewRegistry(agentcore.RegistryOptions{})
	builder := &factoryRecord{}
	registration, err := registry.RegisterFactory(builder)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(registration.Close)
	root, err := registry.Create(
		context.Background(),
		agentcore.CreateOptions{
			SessionID: "closed-before-child-reserve",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err = root.Dispose(context.Background()); err != nil {
		t.Fatal(err)
	}
	_, err = registry.Create(
		context.Background(),
		agentcore.CreateOptions{
			SessionID:     "never-reserved-child",
			RuntimeParent: root.Subject,
		},
	)
	if err == nil || !strings.Contains(err.Error(), "not an exact Agent") {
		t.Fatalf("post-close child Create error = %v", err)
	}
	if builder.createCalls != 1 {
		t.Fatalf("Factory Create calls = %d, want 1", builder.createCalls)
	}
}

func TestChildCloseFailureDoesNotSkipSiblingsOrParent(t *testing.T) {
	t.Parallel()
	childFailure := errors.New("test: child teardown failed")
	registry := agentcore.NewRegistry(agentcore.RegistryOptions{})
	builder := &factoryRecord{
		closeErrors: map[session.SessionID]error{
			"failing-child": childFailure,
		},
	}
	registration, err := registry.RegisterFactory(builder)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(registration.Close)
	root, err := registry.Create(
		context.Background(),
		agentcore.CreateOptions{
			SessionID: "failure-parent",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	for _, childID := range []session.SessionID{"healthy-child", "failing-child"} {
		if _, err = registry.Create(
			context.Background(),
			agentcore.CreateOptions{
				SessionID:     childID,
				RuntimeParent: root.Subject,
			},
		); err != nil {
			t.Fatal(err)
		}
	}
	if err = root.Dispose(context.Background()); !errors.Is(err, childFailure) {
		t.Fatalf("root Dispose error = %v", err)
	}
	if len(builder.closed) != 3 || builder.closed[2] != root.Subject.ID() {
		t.Fatalf("close order = %#v", builder.closed)
	}
	closedChildren := map[session.SessionID]bool{
		builder.closed[0]: true,
		builder.closed[1]: true,
	}
	if !closedChildren["healthy-child"] || !closedChildren["failing-child"] {
		t.Fatalf("sibling close set = %#v", closedChildren)
	}
	if len(registry.List()) != 0 {
		t.Fatalf("failed close left live Agents: %#v", registry.List())
	}
}

func TestOldHandleCannotCloseReplacementEpoch(t *testing.T) {
	t.Parallel()
	registry := agentcore.NewRegistry(agentcore.RegistryOptions{})
	builder := &factoryRecord{}
	registration, err := registry.RegisterFactory(builder)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(registration.Close)
	first, err := registry.Create(
		context.Background(),
		agentcore.CreateOptions{
			SessionID: "reused-session",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err = first.Dispose(context.Background()); err != nil {
		t.Fatal(err)
	}
	second, err := registry.Create(
		context.Background(),
		agentcore.CreateOptions{
			SessionID: "reused-session",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if agentcore.Same(first.Subject, second.Subject) {
		t.Fatal("replacement reused the previous exact Agent identity")
	}
	if err = first.Dispose(context.Background()); err != nil {
		t.Fatal(err)
	}
	if retained, found := registry.Get("reused-session"); !found ||
		!agentcore.Same(retained, second.Subject) {
		t.Fatal("old Handle affected the replacement Agent epoch")
	}
	if err = second.Dispose(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestParentCloseJoinsReservedChildConstruction(t *testing.T) {
	t.Parallel()
	registry := agentcore.NewRegistry(agentcore.RegistryOptions{})
	builder := &joiningFactory{
		childEntered: make(chan struct{}),
		childClosing: make(chan struct{}),
		childRelease: make(chan struct{}),
	}
	registration, err := registry.RegisterFactory(builder)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(registration.Close)
	root, err := registry.Create(
		context.Background(),
		agentcore.CreateOptions{
			SessionID: "joining-parent",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	childDone := make(chan error, 1)
	go func() {
		_, createErr := registry.Create(
			context.Background(),
			agentcore.CreateOptions{
				SessionID:     "joining-child",
				RuntimeParent: root.Subject,
			},
		)
		childDone <- createErr
	}()
	<-builder.childEntered
	parentDone := make(chan error, 1)
	go func() {
		parentDone <- root.Dispose(context.Background())
	}()
	select {
	case <-builder.childClosing:
	case <-time.After(time.Second):
		t.Fatal("parent close did not stop the reserved child construction")
	}
	select {
	case err = <-parentDone:
		t.Fatalf("parent close returned before child construction joined: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(builder.childRelease)
	if err = <-childDone; err == nil ||
		!strings.Contains(err.Error(), "child construction rolled back") {
		t.Fatalf("child Create error = %v", err)
	}
	if err = <-parentDone; err != nil {
		t.Fatal(err)
	}
	if want := []session.SessionID{"joining-parent"}; !reflect.DeepEqual(builder.closed, want) {
		t.Fatalf("close order = %#v, want %#v", builder.closed, want)
	}
}

func TestParentPublicationListenerErrorClosesPublishedDescendantFirst(t *testing.T) {
	t.Parallel()
	registry := agentcore.NewRegistry(agentcore.RegistryOptions{})
	builder := &publicationRejectingFactory{
		registry: registry,
	}
	registration, err := registry.RegisterFactory(builder)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(registration.Close)
	_, err = registry.Create(
		context.Background(),
		agentcore.CreateOptions{
			SessionID: "publishing-parent",
		},
	)
	if err == nil || !strings.Contains(err.Error(), "publication rejected") {
		t.Fatalf("parent Create error = %v", err)
	}
	want := []session.SessionID{
		"publishing-child",
		"publishing-parent",
	}
	if !reflect.DeepEqual(builder.closed, want) {
		t.Fatalf("rollback close order = %#v, want %#v", builder.closed, want)
	}
	if len(registry.List()) != 0 {
		t.Fatalf("publication rollback left live Agents: %#v", registry.List())
	}
	if err = builder.child.Dispose(context.Background()); err != nil {
		t.Fatalf("descendant Handle was not idempotent after parent rollback: %v", err)
	}
}

func TestCloseOwnerCompletesAfterItsContextIsCancelled(t *testing.T) {
	t.Parallel()
	registry := agentcore.NewRegistry(agentcore.RegistryOptions{})
	runtime := &gatedScopeRuntime{
		fakeScopeRuntime: &fakeScopeRuntime{},
		entered:          make(chan struct{}),
		release:          make(chan struct{}),
	}
	registration, err := registry.RegisterFactory(&gatedFactory{
		runtime: runtime,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(registration.Close)
	handleState, err := registry.Create(
		context.Background(),
		agentcore.CreateOptions{
			SessionID: "cancelled-close-owner",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	closeContext, cancelClose := context.WithCancel(context.Background())
	ownerDone := make(chan error, 1)
	go func() {
		ownerDone <- handleState.Dispose(closeContext)
	}()
	<-runtime.entered
	cancelClose()
	waitContext, cancelWait := context.WithCancel(context.Background())
	cancelWait()
	if waitErr := handleState.Dispose(waitContext); !errors.Is(waitErr, context.Canceled) {
		t.Fatalf("concurrent close waiter error = %v", waitErr)
	}
	select {
	case err = <-ownerDone:
		t.Fatalf("close owner abandoned teardown after cancellation: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(runtime.release)
	if err = <-ownerDone; err != nil {
		t.Fatal(err)
	}
	if calls := runtime.calls.Load(); calls != 1 {
		t.Fatalf("raw teardown calls = %d, want 1", calls)
	}
	if _, found := registry.Get(handleState.Subject.ID()); found {
		t.Fatal("completed close left the Agent registered")
	}
}

func TestRegistryShutdownHasOneOwnerAndStableResult(t *testing.T) {
	t.Parallel()
	registry := agentcore.NewRegistry(agentcore.RegistryOptions{})
	runtime := &gatedScopeRuntime{
		fakeScopeRuntime: &fakeScopeRuntime{
			closeErr: errors.New("test: stable Registry shutdown failure"),
		},
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	registration, err := registry.RegisterFactory(&gatedFactory{
		runtime: runtime,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(registration.Close)
	if _, err = registry.Create(
		context.Background(),
		agentcore.CreateOptions{
			SessionID: "registry-shutdown-owner",
		},
	); err != nil {
		t.Fatal(err)
	}
	shutdownDone := make(chan error, 1)
	go func() {
		shutdownDone <- registry.Shutdown(context.Background())
	}()
	<-runtime.entered
	waitContext, cancelWait := context.WithCancel(context.Background())
	cancelWait()
	if waitErr := registry.Shutdown(waitContext); !errors.Is(waitErr, context.Canceled) {
		t.Fatalf("concurrent Shutdown waiter error = %v", waitErr)
	}
	close(runtime.release)
	firstErr := <-shutdownDone
	if firstErr == nil ||
		!strings.Contains(firstErr.Error(), "stable Registry shutdown failure") {
		t.Fatalf("first Shutdown error = %v", firstErr)
	}
	secondErr := registry.Shutdown(context.Background())
	if secondErr == nil || secondErr.Error() != firstErr.Error() {
		t.Fatalf("repeated Shutdown error = %v, want %v", secondErr, firstErr)
	}
	if calls := runtime.calls.Load(); calls != 1 {
		t.Fatalf("raw teardown calls = %d, want 1", calls)
	}
	if len(registry.List()) != 0 {
		t.Fatalf("Shutdown retained Agents: %#v", registry.List())
	}
}

var _ agentcore.Factory = (*factoryRecord)(nil)
var _ agentcore.Factory = (*gatedFactory)(nil)
var _ agentcore.Factory = (*joiningFactory)(nil)
var _ agentcore.Factory = (*publicationRejectingFactory)(nil)
var _ agentcore.AgentScopeRuntime = (*fakeScopeRuntime)(nil)
var _ agentcore.AgentScopeRuntime = (*gatedScopeRuntime)(nil)
var _ agentcore.AgentScopeRuntime = (*publicationRejectingRuntime)(nil)
