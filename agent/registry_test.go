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
	conversation *session.Session
	status       agentcore.Status
}

func (subject *fakeAgent) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: "test-agent:" + string(subject.identifier),
		Provides: []plugin.ServiceType{
			plugin.ServiceOf[agentcore.Agent](),
		},
	}
}

func (*fakeAgent) Apply(context.Context) error   { return nil }
func (*fakeAgent) Dispose(context.Context) error { return nil }
func (subject *fakeAgent) ID() session.SessionID { return subject.identifier }
func (*fakeAgent) OptionsValue() agentcore.Options {
	return agentcore.Options{}
}
func (subject *fakeAgent) SessionValue() *session.Session { return subject.conversation }
func (*fakeAgent) InboxValue() *agentcore.Inbox           { return nil }
func (subject *fakeAgent) StatusValue() agentcore.Status  { return subject.status }
func (*fakeAgent) Cancel(agentcore.CancelCause, agentcore.CancelOptions) {
}
func (*fakeAgent) WhenIdle(context.Context) error { return nil }
func (*fakeAgent) RunMaintenance(
	requestContext context.Context,
	task agentcore.MaintenanceTask,
) error {
	return task.Run(requestContext)
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

func startRegistry(
	t *testing.T,
	plugins ...plugin.Plugin,
) (*plugin.Runtime, *agentcore.RegistryPlugin, plugin.Handle) {
	t.Helper()
	registry := agentcore.NewRegistry(agentcore.RegistryOptions{})
	instances := append([]plugin.Plugin{registry}, plugins...)
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

type lifecycleObserver struct {
	plugin.Base
	name     string
	created  func(agentcore.Agent) error
	disposed func(agentcore.Agent) error
}

func (observer *lifecycleObserver) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: observer.name,
		Events: []plugin.EventSubscription{
			plugin.EventOf[agentcore.Created](),
			plugin.EventOf[agentcore.Disposed](),
		},
	}
}

func (*lifecycleObserver) Apply(context.Context) error   { return nil }
func (*lifecycleObserver) Dispose(context.Context) error { return nil }
func (observer *lifecycleObserver) ObserveEvent(
	_ context.Context,
	fact plugin.Event,
) error {
	switch notice := fact.(type) {
	case agentcore.Created:
		if observer.created != nil {
			return observer.created(notice.Subject)
		}
	case agentcore.Disposed:
		if observer.disposed != nil {
			return observer.disposed(notice.Subject)
		}
	}
	return nil
}

func TestRegistryPublishesExactLifecycleAndRuntimeOwnership(t *testing.T) {
	t.Parallel()
	lifecycle := make([]string, 0)
	observer := &lifecycleObserver{
		name: "lifecycle-observer",
		created: func(subject agentcore.Agent) error {
			lifecycle = append(lifecycle, "created:"+string(subject.ID()))
			return nil
		},
		disposed: func(subject agentcore.Agent) error {
			lifecycle = append(lifecycle, "disposed:"+string(subject.ID()))
			return nil
		},
	}
	runtimeEngine, registry, registryHandle := startRegistry(t, observer)
	root, _ := mountFakeAgent(t, runtimeEngine, registryHandle, "root")
	child, _ := mountFakeAgent(t, runtimeEngine, registryHandle, "child")
	if err := registry.Enter(root, nil); err != nil {
		t.Fatal(err)
	}
	if err := registry.Announce(context.Background(), root); err != nil {
		t.Fatal(err)
	}
	if err := registry.Enter(child, root); err != nil {
		t.Fatal(err)
	}
	if err := registry.Announce(context.Background(), child); err != nil {
		t.Fatal(err)
	}
	if !registry.IsOwnedBy("child", root) || registry.IsOwnedBy("root", root) {
		t.Fatal("runtime ownership projection is incorrect")
	}
	if got := registry.List(); !reflect.DeepEqual(got, []agentcore.Agent{root, child}) {
		t.Fatalf("list = %#v", got)
	}
	if got := registry.Roots(); !reflect.DeepEqual(got, []agentcore.Agent{root}) {
		t.Fatalf("roots = %#v", got)
	}
	if err := registry.Remove(context.Background(), child); err != nil {
		t.Fatal(err)
	}
	if err := registry.Remove(context.Background(), root); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"created:root",
		"created:child",
		"disposed:child",
		"disposed:root",
	}
	if !reflect.DeepEqual(lifecycle, want) {
		t.Fatalf("lifecycle = %#v, want %#v", lifecycle, want)
	}
}

func TestRegistryCreationFailureRollsBackAndPairsDisposal(t *testing.T) {
	t.Parallel()
	lifecycle := make([]string, 0)
	observer := &lifecycleObserver{
		name: "veto-observer",
		created: func(subject agentcore.Agent) error {
			lifecycle = append(lifecycle, "created:"+string(subject.ID()))
			return errors.New("creation veto")
		},
		disposed: func(subject agentcore.Agent) error {
			lifecycle = append(lifecycle, "disposed:"+string(subject.ID()))
			return nil
		},
	}
	runtimeEngine, registry, registryHandle := startRegistry(t, observer)
	subject, _ := mountFakeAgent(t, runtimeEngine, registryHandle, "vetoed")
	if err := registry.Enter(subject, nil); err != nil {
		t.Fatal(err)
	}
	if err := registry.Announce(context.Background(), subject); err == nil {
		t.Fatal("creation veto was ignored")
	}
	if err := registry.Remove(context.Background(), subject); err != nil {
		t.Fatal(err)
	}
	if _, found := registry.Get("vetoed"); found {
		t.Fatal("vetoed Agent remained live")
	}
	want := []string{
		"created:vetoed",
		"disposed:vetoed",
	}
	if !reflect.DeepEqual(lifecycle, want) {
		t.Fatalf("lifecycle = %#v, want %#v", lifecycle, want)
	}
}

func TestRegistryDefersReentrantRemovalUntilCreationDispatchCompletes(t *testing.T) {
	t.Parallel()
	order := make([]string, 0)
	var registry *agentcore.RegistryPlugin
	var expected *fakeAgent
	first := &lifecycleObserver{
		name: "first-created-observer",
		created: func(subject agentcore.Agent) error {
			_, liveBefore := registry.Get(subject.ID())
			order = append(order, "first:"+fmtBool(liveBefore))
			if err := registry.Remove(context.Background(), subject); err != nil {
				return err
			}
			_, liveAfter := registry.Get(subject.ID())
			order = append(order, "after:"+fmtBool(liveAfter))
			return nil
		},
		disposed: func(agentcore.Agent) error {
			order = append(order, "disposed")
			return nil
		},
	}
	second := &lifecycleObserver{
		name: "second-created-observer",
		created: func(subject agentcore.Agent) error {
			order = append(order, "second:"+fmtBool(subject == expected))
			return nil
		},
	}
	runtimeEngine, available, registryHandle := startRegistry(t, first, second)
	registry = available
	expected, _ = mountFakeAgent(t, runtimeEngine, registryHandle, "reentrant")
	if err := registry.Enter(expected, nil); err != nil {
		t.Fatal(err)
	}
	if err := registry.Announce(context.Background(), expected); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"first:true",
		"after:true",
		"second:true",
		"disposed",
	}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("order = %#v, want %#v", order, want)
	}
	if _, found := registry.Get("reentrant"); found {
		t.Fatal("reentrant removal did not remove Agent")
	}
}

func fmtBool(value bool) string {
	if value {
		return "true"
	}
	return "false"
}
