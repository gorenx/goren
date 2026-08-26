package continuable

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/gorenx/goren/agent"
)

func TestCompositeProvisionerRollsBackEarlierParts(t *testing.T) {
	t.Parallel()
	order := make([]string, 0)
	sentinel := errors.New("provision failed")
	configured := &compositeProvisioner{
		parts: []agent.Provisioner{
			provisionerRecord{
				name:  "first",
				order: &order,
			},
			provisionerRecord{
				name:         "second",
				order:        &order,
				provisionErr: sentinel,
			},
		},
	}
	acquired, provisionErr := configured.Provision(context.Background(), nil)
	if acquired != nil || !errors.Is(provisionErr, sentinel) {
		t.Fatalf("Provision = (%v, %v), want nil and sentinel", acquired, provisionErr)
	}
	wanted := []string{
		"provision:first",
		"provision:second",
		"dispose:first",
	}
	if !reflect.DeepEqual(order, wanted) {
		t.Fatalf("lifecycle = %v, want %v", order, wanted)
	}
}

type provisionerRecord struct {
	name         string
	order        *[]string
	provisionErr error
}

func (record provisionerRecord) Provision(
	context.Context,
	agent.Scope,
) (agent.Provisioning, error) {
	*record.order = append(*record.order, "provision:"+record.name)
	if record.provisionErr != nil {
		return nil, record.provisionErr
	}
	return provisioningRecord{
		name:  record.name,
		order: record.order,
	}, nil
}

type provisioningRecord struct {
	name  string
	order *[]string
}

func (record provisioningRecord) Commit() error {
	*record.order = append(*record.order, "commit:"+record.name)
	return nil
}

func (record provisioningRecord) Dispose(context.Context) error {
	*record.order = append(*record.order, "dispose:"+record.name)
	return nil
}

var _ agent.Provisioner = provisionerRecord{}
var _ agent.Provisioning = provisioningRecord{}
