package agent_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

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
}

func (builder *factoryRecord) CreateAgent(
	_ context.Context,
	reservation agentcore.Reservation,
	options agentcore.CreateOptions,
) error {
	builder.createCalls++
	if builder.createErr != nil {
		return builder.createErr
	}
	return builder.attach(reservation, options.SessionID)
}

func (builder *factoryRecord) ResumeAgent(
	_ context.Context,
	reservation agentcore.Reservation,
	options agentcore.ResumeOptions,
) error {
	return builder.attach(reservation, options.SessionID)
}

func (builder *factoryRecord) attach(
	reservation agentcore.Reservation,
	identifier session.SessionID,
) error {
	subject := newFakeAgentForFactory(identifier)
	runtime := &fakeScopeRuntime{
		closed:  &builder.closed,
		subject: subject,
	}
	subject.runtime = runtime
	_, err := reservation.Attach(subject, runtime)
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
	firstRegistration, err := registry.RegisterFactory(first)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = registry.RegisterFactory(&factoryRecord{}); err == nil {
		t.Fatal("Registry accepted a second Factory")
	}
	firstRegistration.Unregister()

	sentinel := errors.New("second Factory called")
	second := &factoryRecord{
		createErr: sentinel,
	}
	secondRegistration, err := registry.RegisterFactory(second)
	if err != nil {
		t.Fatal(err)
	}
	_, err = registry.Create(
		context.Background(),
		agentcore.CreateOptions{
			SessionID: "second",
		},
	)
	if !errors.Is(err, sentinel) || second.createCalls != 1 {
		t.Fatalf("Create result = (%d calls, %v)", second.createCalls, err)
	}
	secondRegistration.Unregister()
}

func TestRegistryOwnsRuntimeParentAndClosesChildFirst(t *testing.T) {
	t.Parallel()
	registry := agentcore.NewRegistry(agentcore.RegistryOptions{})
	builder := &factoryRecord{}
	registration, err := registry.RegisterFactory(builder)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(registration.Unregister)
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

func TestRegistryCloseDescendantsClosesAdmission(t *testing.T) {
	t.Parallel()
	registry := agentcore.NewRegistry(agentcore.RegistryOptions{})
	builder := &factoryRecord{}
	registration, err := registry.RegisterFactory(builder)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(registration.Unregister)
	root, err := registry.Create(
		context.Background(),
		agentcore.CreateOptions{
			SessionID: "root-admission",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err = registry.CloseDescendants(context.Background(), root.Subject); err != nil {
		t.Fatal(err)
	}
	if _, err = registry.Create(
		context.Background(),
		agentcore.CreateOptions{
			SessionID:     "late-child",
			RuntimeParent: root.Subject,
		},
	); err == nil {
		t.Fatal("Registry accepted a child after descendant admission closed")
	}
	if err = root.Dispose(context.Background()); err != nil {
		t.Fatal(err)
	}
}

var _ agentcore.Factory = (*factoryRecord)(nil)
var _ agentcore.AgentScopeRuntime = (*fakeScopeRuntime)(nil)
