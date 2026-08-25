package agent_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/gorenx/goren/agent"
)

type provisioningScope struct {
	resources []agent.ScopeResource
	ownErr    error
}

func (*provisioningScope) Agent() agent.Agent { return nil }

func (scope *provisioningScope) Own(resource agent.ScopeResource) error {
	if scope.ownErr != nil {
		return scope.ownErr
	}
	scope.resources = append(scope.resources, resource)
	return nil
}

type provisioningAction struct {
	result agent.Provisioning
	err    error
}

func (action *provisioningAction) Provision(
	context.Context,
	agent.Scope,
) (agent.Provisioning, error) {
	return action.result, action.err
}

type provisioningTransaction struct {
	order     *[]string
	commitErr error
}

func (transaction *provisioningTransaction) Commit() error {
	*transaction.order = append(*transaction.order, "commit")
	return transaction.commitErr
}

func (transaction *provisioningTransaction) Dispose(context.Context) error {
	*transaction.order = append(*transaction.order, "dispose")
	return nil
}

func TestApplyProvisioningTransfersThenCommits(t *testing.T) {
	t.Parallel()
	order := make([]string, 0, 1)
	transaction := &provisioningTransaction{
		order: &order,
	}
	scope := &provisioningScope{}
	err := agent.ApplyProvisioning(
		context.Background(),
		scope,
		&provisioningAction{
			result: transaction,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(order, []string{"commit"}) ||
		len(scope.resources) != 1 || scope.resources[0] != transaction {
		t.Fatalf("order=%v resources=%#v", order, scope.resources)
	}
}

func TestApplyProvisioningDisposesWhenOwnershipTransferFails(t *testing.T) {
	t.Parallel()
	order := make([]string, 0, 1)
	transferErr := errors.New("scope closing")
	transaction := &provisioningTransaction{
		order: &order,
	}
	err := agent.ApplyProvisioning(
		context.Background(),
		&provisioningScope{
			ownErr: transferErr,
		},
		&provisioningAction{
			result: transaction,
		},
	)
	if !errors.Is(err, transferErr) ||
		!reflect.DeepEqual(order, []string{"dispose"}) {
		t.Fatalf("error=%v order=%v", err, order)
	}
}

var _ agent.Scope = (*provisioningScope)(nil)
var _ agent.Provisioner = (*provisioningAction)(nil)
var _ agent.Provisioning = (*provisioningTransaction)(nil)
