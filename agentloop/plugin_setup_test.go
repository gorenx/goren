package agentloop_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/gorenx/goren/agent"
)

type scopeSetupRecord struct {
	registry agent.Registry
	order    *[]string
	checkErr error
}

func (record *scopeSetupRecord) Apply(
	_ context.Context,
	subject agent.Agent,
	editor agent.ScopeEditor,
) error {
	*record.order = append(*record.order, "apply")
	if record.registry.Contains(subject) {
		return errors.New("Agent was published before Setup")
	}
	if err := editor.Own(&scopeEffectRecord{
		order: record.order,
	}); err != nil {
		return err
	}
	return editor.Check(&scopeCheckRecord{
		order: record.order,
		err:   record.checkErr,
	})
}

type scopeCheckRecord struct {
	order *[]string
	err   error
}

func (validation *scopeCheckRecord) Check() error {
	*validation.order = append(*validation.order, "check")
	return validation.err
}

type scopeEffectRecord struct {
	order *[]string
}

func (effect *scopeEffectRecord) Close(context.Context) error {
	*effect.order = append(*effect.order, "effect:close")
	return nil
}

type cancelingSetup struct {
	cancel context.CancelFunc
	closed bool
}

func (setup *cancelingSetup) Apply(
	requestContext context.Context,
	_ agent.Agent,
	editor agent.ScopeEditor,
) error {
	if err := editor.Own(&flagScopeResource{
		closed: &setup.closed,
	}); err != nil {
		return err
	}
	setup.cancel()
	<-requestContext.Done()
	return context.Cause(requestContext)
}

type flagScopeResource struct {
	closed *bool
}

func (resource *flagScopeResource) Close(context.Context) error {
	*resource.closed = true
	return nil
}

func TestAgentSetupChecksBeforePublicationAndOwnsResources(t *testing.T) {
	state := newHarnessFixture(t, nil)
	order := make([]string, 0)
	handle, err := state.agents.Create(
		context.Background(),
		agent.CreateOptions{
			SessionID: "setup-publication",
			AgentOptions: agent.Options{
				Provider: "mock",
				Model:    "model",
			},
			Setup: &scopeSetupRecord{
				registry: state.agents,
				order:    &order,
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(order, []string{"apply", "check"}) {
		t.Fatalf("Setup order before close = %v", order)
	}
	if !state.agents.Contains(handle.Subject) {
		t.Fatal("Agent was not published after Setup check")
	}
	if err = handle.Dispose(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(order, []string{"apply", "check", "effect:close"}) {
		t.Fatalf("Setup lifecycle = %v", order)
	}
}

func TestAgentSetupCheckFailureRollsBackWithoutPublication(t *testing.T) {
	state := newHarnessFixture(t, nil)
	order := make([]string, 0)
	wantErr := errors.New("Setup invalidated")
	_, err := state.agents.Create(
		context.Background(),
		agent.CreateOptions{
			SessionID: "setup-rollback",
			AgentOptions: agent.Options{
				Provider: "mock",
				Model:    "model",
			},
			Setup: &scopeSetupRecord{
				registry: state.agents,
				order:    &order,
				checkErr: wantErr,
			},
		},
	)
	if !errors.Is(err, wantErr) {
		t.Fatalf("Create error = %v", err)
	}
	if _, found := state.agents.Get("setup-rollback"); found {
		t.Fatal("check-failed Agent remained published")
	}
	if _, found := state.sessions.Get("setup-rollback"); found {
		t.Fatal("check-failed Session remained published")
	}
	if !reflect.DeepEqual(order, []string{"apply", "check", "effect:close"}) {
		t.Fatalf("Setup rollback = %v", order)
	}
}

func TestAgentSetupCancellationRollsBackResources(t *testing.T) {
	state := newHarnessFixture(t, nil)
	requestContext, cancelRequest := context.WithCancel(context.Background())
	configured := &cancelingSetup{
		cancel: cancelRequest,
	}
	_, err := state.agents.Create(
		requestContext,
		agent.CreateOptions{
			SessionID: "setup-canceled",
			AgentOptions: agent.Options{
				Provider: "mock",
				Model:    "model",
			},
			Setup: configured,
		},
	)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Create error = %v, want canceled", err)
	}
	if !configured.closed {
		t.Fatal("failed Setup retained an acquired resource")
	}
}

var _ agent.Setup = (*scopeSetupRecord)(nil)
var _ agent.Setup = (*cancelingSetup)(nil)
var _ agent.ScopeCheck = (*scopeCheckRecord)(nil)
var _ agent.ScopeResource = (*scopeEffectRecord)(nil)
var _ agent.ScopeResource = (*flagScopeResource)(nil)
