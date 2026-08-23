package agentloop_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/gorenx/goren/agent"
)

type setupRecord struct {
	registry  agent.Registry
	order     *[]string
	commitErr error
}

func (setup *setupRecord) Prepare(
	_ context.Context,
	scope agent.Scope,
) error {
	*setup.order = append(*setup.order, "prepare")
	if setup.registry.Contains(scope.AgentValue()) {
		return errors.New("Agent was published before Setup.Prepare")
	}
	return scope.Own(&effectRecord{
		order: setup.order,
	})
}

func (setup *setupRecord) Commit() error {
	*setup.order = append(*setup.order, "commit")
	return setup.commitErr
}

func (setup *setupRecord) Dispose(context.Context) error {
	*setup.order = append(*setup.order, "setup:dispose")
	return nil
}

type effectRecord struct {
	order *[]string
}

func (effect *effectRecord) Dispose(context.Context) error {
	*effect.order = append(*effect.order, "effect:dispose")
	return nil
}

type cancelingSetup struct {
	cancel   context.CancelFunc
	disposed bool
}

func (setup *cancelingSetup) Prepare(
	requestContext context.Context,
	_ agent.Scope,
) error {
	setup.cancel()
	<-requestContext.Done()
	return context.Cause(requestContext)
}

func (*cancelingSetup) Commit() error {
	return nil
}

func (setup *cancelingSetup) Dispose(context.Context) error {
	setup.disposed = true
	return nil
}

func TestAgentSetupCommitsBeforePublicationAndOwnsEffects(t *testing.T) {
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
			Setup: &setupRecord{
				registry: state.agents,
				order:    &order,
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(order, []string{"prepare", "commit"}) {
		t.Fatalf("setup order before Dispose = %v", order)
	}
	if !state.agents.Contains(handle.Subject) {
		t.Fatal("Agent was not published after Setup.Commit")
	}
	if err = handle.Dispose(context.Background()); err != nil {
		t.Fatal(err)
	}
	wanted := []string{
		"prepare",
		"commit",
		"effect:dispose",
		"setup:dispose",
	}
	if !reflect.DeepEqual(order, wanted) {
		t.Fatalf("setup lifecycle = %v, want %v", order, wanted)
	}
}

func TestAgentSetupCommitFailureRollsBackWithoutPublication(t *testing.T) {
	state := newHarnessFixture(t, nil)
	order := make([]string, 0)
	sentinel := errors.New("setup invalidated")
	_, err := state.agents.Create(
		context.Background(),
		agent.CreateOptions{
			SessionID: "setup-rollback",
			AgentOptions: agent.Options{
				Provider: "mock",
				Model:    "model",
			},
			Setup: &setupRecord{
				registry:  state.agents,
				order:     &order,
				commitErr: sentinel,
			},
		},
	)
	if !errors.Is(err, sentinel) {
		t.Fatalf("Create error = %v", err)
	}
	if _, found := state.agents.Get("setup-rollback"); found {
		t.Fatal("commit-failed Agent remained published")
	}
	if _, found := state.sessions.Get("setup-rollback"); found {
		t.Fatal("commit-failed Session remained published")
	}
	wanted := []string{
		"prepare",
		"commit",
		"effect:dispose",
		"setup:dispose",
	}
	if !reflect.DeepEqual(order, wanted) {
		t.Fatalf("setup rollback = %v, want %v", order, wanted)
	}
}

func TestAgentSetupCancellationStillRollsBackTree(t *testing.T) {
	state := newHarnessFixture(t, nil)
	baselineStatuses := len(state.runtimeEngine.Statuses())
	requestContext, cancelRequest := context.WithCancel(context.Background())
	setup := &cancelingSetup{
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
			Setup: setup,
		},
	)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Create error = %v, want canceled", err)
	}
	if !setup.disposed {
		t.Fatal("canceled Setup was not disposed")
	}
	if _, found := state.agents.Get("setup-canceled"); found {
		t.Fatal("canceled Agent remained published")
	}
	if _, found := state.sessions.Get("setup-canceled"); found {
		t.Fatal("canceled Session remained published")
	}
	if got := len(state.runtimeEngine.Statuses()); got != baselineStatuses {
		t.Fatalf("Runtime retained %d canceled tree nodes", got-baselineStatuses)
	}
}

var _ agent.Setup = (*setupRecord)(nil)
var _ agent.Setup = (*cancelingSetup)(nil)
var _ agent.Effect = (*effectRecord)(nil)
