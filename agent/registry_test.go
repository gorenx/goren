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

type registryPlugin struct {
	ready func(agentcore.Registry, *plugin.Scope) error
}

func (registryPlugin) Manifest() plugin.Manifest {
	return plugin.Manifest{Name: "agent-registry-fixture", Provides: []plugin.ServiceRef{agentcore.Service.Ref()}}
}

func (instance registryPlugin) Apply(_ context.Context, pluginScope *plugin.Scope) error {
	serviceValue, err := agentcore.NewRegistry(pluginScope, agentcore.RegistryOptions{})
	if err != nil {
		return err
	}
	if _, err := plugin.Provide(pluginScope, agentcore.Service, serviceValue); err != nil {
		return err
	}
	if instance.ready != nil {
		return instance.ready(serviceValue, pluginScope)
	}
	return nil
}

type fakeAgent struct {
	identifier   session.SessionID
	conversation *session.Session
	pending      *agentcore.Inbox
	agentScope   *plugin.Scope
	status       agentcore.Status
}

func (subject *fakeAgent) ID() session.SessionID                         { return subject.identifier }
func (*fakeAgent) OptionsValue() agentcore.Options                       { return agentcore.Options{} }
func (subject *fakeAgent) SessionValue() *session.Session                { return subject.conversation }
func (subject *fakeAgent) InboxValue() *agentcore.Inbox                  { return subject.pending }
func (subject *fakeAgent) StatusValue() agentcore.Status                 { return subject.status }
func (subject *fakeAgent) ScopeValue() *plugin.Scope                     { return subject.agentScope }
func (*fakeAgent) Cancel(agentcore.CancelCause, agentcore.CancelOptions) {}
func (*fakeAgent) WhenIdle(context.Context) error                        { return nil }
func (*fakeAgent) RunMaintenance(requestContext context.Context, task agentcore.MaintenanceTask) error {
	return task.Run(requestContext)
}
func (*fakeAgent) Send(llm.UserMessage, agentcore.InboxTarget, bool) error { return nil }
func (*fakeAgent) Followup(llm.UserMessage) error                          { return nil }
func (*fakeAgent) Steer(llm.UserMessage) error                             { return nil }
func (*fakeAgent) Inject(llm.UserMessage) error                            { return nil }

func mountRegistry(t *testing.T, ready func(agentcore.Registry, *plugin.Scope) error) (*plugin.Runtime, agentcore.Registry, *plugin.Scope) {
	t.Helper()
	engine := plugin.NewRuntime()
	var serviceValue agentcore.Registry
	var providerScope *plugin.Scope
	_, err := engine.Load(context.Background(), registryPlugin{ready: func(available agentcore.Registry, pluginScope *plugin.Scope) error {
		serviceValue = available
		providerScope = pluginScope
		if ready != nil {
			return ready(available, pluginScope)
		}
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	return engine, serviceValue, providerScope
}

func newFakeAgent(t *testing.T, providerScope *plugin.Scope, identifier session.SessionID) (*fakeAgent, plugin.Disposer) {
	t.Helper()
	agentScope, releaseScope, err := providerScope.Child(string(identifier))
	if err != nil {
		t.Fatal(err)
	}
	conversation, err := session.New(identifier, session.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return &fakeAgent{identifier: identifier, conversation: conversation, agentScope: agentScope, status: agentcore.StatusIdle}, releaseScope
}

func TestRegistryPublishesExactLifecycleAndRuntimeOwnership(t *testing.T) {
	t.Parallel()
	lifecycle := []string{}
	_, serviceValue, providerScope := mountRegistry(t, func(_ agentcore.Registry, pluginScope *plugin.Scope) error {
		if _, err := agentcore.OnCreated(pluginScope, func(_ context.Context, subject agentcore.Agent) error {
			lifecycle = append(lifecycle, "created:"+string(subject.ID()))
			return nil
		}); err != nil {
			return err
		}
		_, err := agentcore.OnDisposed(pluginScope, func(_ context.Context, subject agentcore.Agent) error {
			lifecycle = append(lifecycle, "disposed:"+string(subject.ID()))
			return nil
		})
		return err
	})
	root, _ := newFakeAgent(t, providerScope, "root")
	child, _ := newFakeAgent(t, providerScope, "child")
	detachRoot, err := serviceValue.Enter(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := serviceValue.Announce(context.Background(), root); err != nil {
		t.Fatal(err)
	}
	detachChild, err := serviceValue.Enter(child, root)
	if err != nil {
		t.Fatal(err)
	}
	if err := serviceValue.Announce(context.Background(), child); err != nil {
		t.Fatal(err)
	}
	if !serviceValue.IsOwnedBy("child", root) || serviceValue.IsOwnedBy("root", root) {
		t.Fatal("runtime ownership projection is incorrect")
	}
	if got := serviceValue.List(); !reflect.DeepEqual(got, []agentcore.Agent{root, child}) {
		t.Fatalf("list = %#v", got)
	}
	if got := serviceValue.Roots(); !reflect.DeepEqual(got, []agentcore.Agent{root}) {
		t.Fatalf("roots = %#v", got)
	}
	if err := detachChild(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := detachRoot(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := []string{"created:root", "created:child", "disposed:child", "disposed:root"}
	if !reflect.DeepEqual(lifecycle, want) {
		t.Fatalf("lifecycle = %#v, want %#v", lifecycle, want)
	}
}

func TestRegistryCreationFailureRollsBackAndPairsDisposal(t *testing.T) {
	t.Parallel()
	lifecycle := []string{}
	_, serviceValue, providerScope := mountRegistry(t, func(_ agentcore.Registry, pluginScope *plugin.Scope) error {
		if _, err := agentcore.OnCreated(pluginScope, func(_ context.Context, subject agentcore.Agent) error {
			lifecycle = append(lifecycle, "created:"+string(subject.ID()))
			return errors.New("creation veto")
		}); err != nil {
			return err
		}
		_, err := agentcore.OnDisposed(pluginScope, func(_ context.Context, subject agentcore.Agent) error {
			lifecycle = append(lifecycle, "disposed:"+string(subject.ID()))
			return nil
		})
		return err
	})
	subject, _ := newFakeAgent(t, providerScope, "vetoed")
	if _, err := serviceValue.Register(context.Background(), providerScope, subject, nil); err == nil {
		t.Fatal("creation veto was ignored")
	}
	if _, found := serviceValue.Get("vetoed"); found {
		t.Fatal("vetoed Agent remained live")
	}
	if want := []string{"created:vetoed", "disposed:vetoed"}; !reflect.DeepEqual(lifecycle, want) {
		t.Fatalf("lifecycle = %#v, want %#v", lifecycle, want)
	}
}

func TestRegistryDefersReentrantDetachUntilCreationDispatchCompletes(t *testing.T) {
	t.Parallel()
	order := []string{}
	var detach plugin.Disposer
	var observed agentcore.Registry
	var expected *fakeAgent
	_, serviceValue, providerScope := mountRegistry(t, func(available agentcore.Registry, pluginScope *plugin.Scope) error {
		observed = available
		if _, err := agentcore.OnCreated(pluginScope, func(_ context.Context, subject agentcore.Agent) error {
			_, liveBefore := observed.Get(subject.ID())
			order = append(order, "first:"+fmtBool(liveBefore))
			if err := detach(context.Background()); err != nil {
				return err
			}
			_, liveAfter := observed.Get(subject.ID())
			order = append(order, "after:"+fmtBool(liveAfter))
			return nil
		}); err != nil {
			return err
		}
		if _, err := agentcore.OnCreated(pluginScope, func(_ context.Context, subject agentcore.Agent) error {
			order = append(order, "second:"+fmtBool(subject == expected))
			return nil
		}); err != nil {
			return err
		}
		_, err := agentcore.OnDisposed(pluginScope, func(context.Context, agentcore.Agent) error {
			order = append(order, "disposed")
			return nil
		})
		return err
	})
	expected, _ = newFakeAgent(t, providerScope, "reentrant")
	var err error
	detach, err = serviceValue.Enter(expected, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := serviceValue.Announce(context.Background(), expected); err != nil {
		t.Fatal(err)
	}
	want := []string{"first:true", "after:true", "second:true", "disposed"}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("order = %#v, want %#v", order, want)
	}
	if _, found := serviceValue.Get("reentrant"); found {
		t.Fatal("reentrant detach did not remove Agent")
	}
}

func fmtBool(value bool) string {
	if value {
		return "true"
	}
	return "false"
}
