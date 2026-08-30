package agent_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/gorenx/goren/agent"
)

type setupRecord struct {
	name  string
	order *[]string
	err   error
}

func (setup setupRecord) Apply(
	context.Context,
	agent.Agent,
	agent.ScopeEditor,
) error {
	*setup.order = append(*setup.order, setup.name)
	return setup.err
}

func TestComposeSetupsAppliesInDeclaredOrder(t *testing.T) {
	t.Parallel()
	order := []string{}
	composed := agent.ComposeSetups(
		setupRecord{
			name:  "first",
			order: &order,
		},
		nil,
		setupRecord{
			name:  "second",
			order: &order,
		},
	)
	if composed == nil {
		t.Fatal("composed Setup is nil")
	}
	if err := composed.Apply(context.Background(), nil, nil); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(order, []string{"first", "second"}) {
		t.Fatalf("Setup order = %v", order)
	}
}

func TestComposeSetupsStopsAtFirstFailure(t *testing.T) {
	t.Parallel()
	order := []string{}
	wantErr := errors.New("stop")
	composed := agent.ComposeSetups(
		setupRecord{
			name:  "first",
			order: &order,
			err:   wantErr,
		},
		setupRecord{
			name:  "second",
			order: &order,
		},
	)
	if err := composed.Apply(context.Background(), nil, nil); !errors.Is(err, wantErr) {
		t.Fatalf("Apply error = %v", err)
	}
	if !reflect.DeepEqual(order, []string{"first"}) {
		t.Fatalf("Setup order = %v", order)
	}
}

func TestComposeSetupsReturnsNilForNoContributions(t *testing.T) {
	t.Parallel()
	if agent.ComposeSetups(nil, nil) != nil {
		t.Fatal("empty composition returned a Setup")
	}
}

var _ agent.Setup = setupRecord{}
