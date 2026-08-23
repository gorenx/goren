package composition

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/subagent"
	"github.com/gorenx/goren/subagent/internal/continuation"
	"github.com/gorenx/goren/tools"
)

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

func TestComposerKeepsDeploymentCompositionOnColdResume(t *testing.T) {
	personaText := "review carefully"
	owner := NewContinuable(nil, nil)
	descriptor := subagent.ContinuableDescriptor{
		Persona: &personaText,
		ToolFilter: &tools.ToolRestriction{
			Allow: []string{"read"},
		},
	}
	fresh := owner.buildPlugins(
		continuation.Composition{
			Descriptor: descriptor,
			Fresh:      true,
		},
	)
	resumed := owner.buildPlugins(
		continuation.Composition{
			Descriptor: descriptor,
			Fresh:      false,
		},
	)
	if len(fresh) != 2 || len(resumed) != 2 {
		t.Fatalf(
			"plugins fresh=%d resumed=%d, want persona and restriction",
			len(fresh),
			len(resumed),
		)
	}
	if _, matches := resumed[0].(*persona); !matches {
		t.Fatalf("first resumed Plugin = %T, want persona", resumed[0])
	}
	if _, matches := resumed[1].(*toolRestriction); !matches {
		t.Fatalf("second resumed Plugin = %T, want toolRestriction", resumed[1])
	}
}

func TestCompositionDisposesProvisioningWhenLaterPartFails(t *testing.T) {
	order := make([]string, 0)
	sentinel := errors.New("provision failed")
	configured := &provisioner{
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

var _ agent.Provisioner = provisionerRecord{}
var _ agent.Provisioning = provisioningRecord{}
