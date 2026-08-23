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

type setupRecord struct {
	name       string
	order      *[]string
	prepareErr error
}

func (setup setupRecord) Prepare(context.Context, agent.Scope) error {
	*setup.order = append(*setup.order, "prepare:"+setup.name)
	return setup.prepareErr
}

func (setup setupRecord) Commit() error {
	*setup.order = append(*setup.order, "commit:"+setup.name)
	return nil
}

func (setup setupRecord) Dispose(context.Context) error {
	*setup.order = append(*setup.order, "dispose:"+setup.name)
	return nil
}

func TestComposerKeepsDeploymentCompositionOnColdResume(t *testing.T) {
	personaText := "review carefully"
	owner := New(nil, nil)
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

func TestCompositionDisposesPartiallyPreparedPart(t *testing.T) {
	order := make([]string, 0)
	sentinel := errors.New("prepare failed")
	setup := &composition{
		parts: []agent.Setup{
			setupRecord{
				name:  "first",
				order: &order,
			},
			setupRecord{
				name:       "second",
				order:      &order,
				prepareErr: sentinel,
			},
		},
	}
	if prepareErr := setup.Prepare(context.Background(), nil); !errors.Is(prepareErr, sentinel) {
		t.Fatalf("Prepare error = %v", prepareErr)
	}
	if closeErr := setup.Dispose(context.Background()); closeErr != nil {
		t.Fatal(closeErr)
	}
	wanted := []string{
		"prepare:first",
		"prepare:second",
		"dispose:second",
		"dispose:first",
	}
	if !reflect.DeepEqual(order, wanted) {
		t.Fatalf("lifecycle = %v, want %v", order, wanted)
	}
}

var _ agent.Setup = setupRecord{}
