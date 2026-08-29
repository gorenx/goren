package agentloop_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/gorenx/goren/agent"
)

type provisionerRecord struct {
	registry  agent.Registry
	order     *[]string
	commitErr error
}

func (record *provisionerRecord) Provision(
	_ context.Context,
	scope agent.Scope,
) (agent.Provisioning, error) {
	*record.order = append(*record.order, "provision")
	if record.registry.Contains(scope.Agent()) {
		return nil, errors.New("Agent was published before provisioning")
	}
	if err := scope.Own(&effectRecord{
		order: record.order,
	}); err != nil {
		return nil, err
	}
	return &provisioningRecord{
		order:     record.order,
		commitErr: record.commitErr,
	}, nil
}

type provisioningRecord struct {
	order     *[]string
	commitErr error
}

func (record *provisioningRecord) Commit() error {
	*record.order = append(*record.order, "commit")
	return record.commitErr
}

func (record *provisioningRecord) Dispose(context.Context) error {
	*record.order = append(*record.order, "provisioning:dispose")
	return nil
}

type effectRecord struct {
	order *[]string
}

func (effect *effectRecord) Dispose(context.Context) error {
	*effect.order = append(*effect.order, "effect:dispose")
	return nil
}

type cancelingProvisioner struct {
	cancel              context.CancelFunc
	transferredDisposed bool
}

func (owner *cancelingProvisioner) Provision(
	requestContext context.Context,
	scope agent.Scope,
) (agent.Provisioning, error) {
	if err := scope.Own(&flagEffect{
		disposed: &owner.transferredDisposed,
	}); err != nil {
		return nil, err
	}
	owner.cancel()
	<-requestContext.Done()
	return nil, context.Cause(requestContext)
}

type flagEffect struct {
	disposed *bool
}

func (effect *flagEffect) Dispose(context.Context) error {
	*effect.disposed = true
	return nil
}

func TestAgentProvisioningCommitsBeforePublicationAndOwnsEffects(t *testing.T) {
	state := newHarnessFixture(t, nil)
	order := make([]string, 0)
	handle, err := state.agents.Create(
		context.Background(),
		agent.CreateOptions{
			SessionID: "provisioning-publication",
			AgentOptions: agent.Options{
				Provider: "mock",
				Model:    "model",
			},
			Provisioner: &provisionerRecord{
				registry: state.agents,
				order:    &order,
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(order, []string{"provision", "commit"}) {
		t.Fatalf("provisioning order before Dispose = %v", order)
	}
	if !state.agents.Contains(handle.Subject) {
		t.Fatal("Agent was not published after Provisioning.Commit")
	}
	if err = handle.Dispose(context.Background()); err != nil {
		t.Fatal(err)
	}
	wanted := []string{
		"provision",
		"commit",
		"provisioning:dispose",
		"effect:dispose",
	}
	if !reflect.DeepEqual(order, wanted) {
		t.Fatalf("provisioning lifecycle = %v, want %v", order, wanted)
	}
}

func TestAgentProvisioningCommitFailureRollsBackWithoutPublication(t *testing.T) {
	state := newHarnessFixture(t, nil)
	order := make([]string, 0)
	sentinel := errors.New("provisioning invalidated")
	_, err := state.agents.Create(
		context.Background(),
		agent.CreateOptions{
			SessionID: "provisioning-rollback",
			AgentOptions: agent.Options{
				Provider: "mock",
				Model:    "model",
			},
			Provisioner: &provisionerRecord{
				registry:  state.agents,
				order:     &order,
				commitErr: sentinel,
			},
		},
	)
	if !errors.Is(err, sentinel) {
		t.Fatalf("Create error = %v", err)
	}
	if _, found := state.agents.Get("provisioning-rollback"); found {
		t.Fatal("commit-failed Agent remained published")
	}
	if _, found := state.sessions.Get("provisioning-rollback"); found {
		t.Fatal("commit-failed Session remained published")
	}
	wanted := []string{
		"provision",
		"commit",
		"provisioning:dispose",
		"effect:dispose",
	}
	if !reflect.DeepEqual(order, wanted) {
		t.Fatalf("provisioning rollback = %v, want %v", order, wanted)
	}
}

func TestAgentProvisioningCancellationStillRollsBackTree(t *testing.T) {
	state := newHarnessFixture(t, nil)
	baselineStatuses := len(state.runtimeEngine.Statuses())
	requestContext, cancelRequest := context.WithCancel(context.Background())
	configured := &cancelingProvisioner{
		cancel: cancelRequest,
	}
	_, err := state.agents.Create(
		requestContext,
		agent.CreateOptions{
			SessionID: "provisioning-canceled",
			AgentOptions: agent.Options{
				Provider: "mock",
				Model:    "model",
			},
			Provisioner: configured,
		},
	)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Create error = %v, want canceled", err)
	}
	if !configured.transferredDisposed {
		t.Fatal("failed Provisioner retained a resource transferred to the Scope")
	}
	if _, found := state.agents.Get("provisioning-canceled"); found {
		t.Fatal("canceled Agent remained published")
	}
	if _, found := state.sessions.Get("provisioning-canceled"); found {
		t.Fatal("canceled Session remained published")
	}
	if got := len(state.runtimeEngine.Statuses()); got != baselineStatuses {
		t.Fatalf("Runtime retained %d canceled tree nodes", got-baselineStatuses)
	}
}

var _ agent.Provisioner = (*provisionerRecord)(nil)
var _ agent.Provisioner = (*cancelingProvisioner)(nil)
var _ agent.Provisioning = (*provisioningRecord)(nil)
var _ agent.ScopeResource = (*effectRecord)(nil)
var _ agent.ScopeResource = (*flagEffect)(nil)
