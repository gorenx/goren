package plugin

import (
	"context"
	"reflect"
	"testing"

	"github.com/gorenx/goren/agent"
)

func TestEnvironmentTransfersOnlyExtensionProvisioning(t *testing.T) {
	t.Parallel()
	order := make([]string, 0)
	configured := &continuableEnvironment{
		policies: provisionerRecord{
			name:  "policy",
			order: &order,
		},
		extensions: provisionerRecord{
			name:               "extension",
			order:              &order,
			returnProvisioning: true,
		},
	}
	acquired, provisionErr := configured.Provision(context.Background(), nil)
	if provisionErr != nil || acquired == nil {
		t.Fatalf(
			"Provision = (%v, %v), want Extension Provisioning",
			acquired,
			provisionErr,
		)
	}
	if commitErr := acquired.Commit(); commitErr != nil {
		t.Fatal(commitErr)
	}
	if disposeErr := acquired.Dispose(context.Background()); disposeErr != nil {
		t.Fatal(disposeErr)
	}
	wanted := []string{
		"provision:policy",
		"provision:extension",
		"commit:extension",
		"dispose:extension",
	}
	if !reflect.DeepEqual(order, wanted) {
		t.Fatalf("lifecycle = %v, want %v", order, wanted)
	}
}

type provisionerRecord struct {
	name               string
	order              *[]string
	returnProvisioning bool
}

func (record provisionerRecord) Provision(
	context.Context,
	agent.Scope,
) (agent.Provisioning, error) {
	*record.order = append(*record.order, "provision:"+record.name)
	if !record.returnProvisioning {
		return nil, nil
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
