package setup

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/subagent"
)

type scopeRecord struct{}

func (scopeRecord) Agent() agent.Agent { return nil }

func (scopeRecord) Mount(
	context.Context,
	plugin.Plugin,
) (agent.Effect, error) {
	return nil, errors.New("unused")
}

func (scopeRecord) Own(agent.Effect) error { return nil }

type setupRecord struct {
	install func() (subagent.Installation, error)
}

func (record setupRecord) Install(
	context.Context,
	subagent.ActivationContext,
) (subagent.Installation, error) {
	return record.install()
}

type installationRecord struct {
	dispose func() error
}

func (record installationRecord) Uninstall(context.Context) error {
	return record.dispose()
}

func TestRegistryComposesInOrderAndReleasesWithChild(t *testing.T) {
	owner := New()
	order := make([]string, 0)
	for _, name := range []string{"first", "second"} {
		name := name
		if _, registerErr := owner.Register(
			setupRecord{
				install: func() (subagent.Installation, error) {
					order = append(order, name)
					return installationRecord{
						dispose: func() error {
							order = append(order, "undo-"+name)
							return nil
						},
					}, nil
				},
			},
		); registerErr != nil {
			t.Fatal(registerErr)
		}
	}
	setup := owner.Compose(Input{})
	if prepareErr := setup.Prepare(context.Background(), scopeRecord{}); prepareErr != nil {
		t.Fatal(prepareErr)
	}
	if commitErr := setup.Commit(); commitErr != nil {
		t.Fatal(commitErr)
	}
	if closeErr := setup.Dispose(context.Background()); closeErr != nil {
		t.Fatal(closeErr)
	}
	wanted := []string{"first", "second", "undo-first", "undo-second"}
	if !reflect.DeepEqual(order, wanted) {
		t.Fatalf("Setup order = %v, want %v", order, wanted)
	}
}

func TestRegistrationRemovalInvalidatesUnpublishedSetup(t *testing.T) {
	owner := New()
	disposals := 0
	handle, registerErr := owner.Register(
		setupRecord{
			install: func() (subagent.Installation, error) {
				return installationRecord{
					dispose: func() error {
						disposals++
						return nil
					},
				}, nil
			},
		},
	)
	if registerErr != nil {
		t.Fatal(registerErr)
	}
	setup := owner.Compose(Input{})
	if prepareErr := setup.Prepare(context.Background(), scopeRecord{}); prepareErr != nil {
		t.Fatal(prepareErr)
	}
	if removeErr := handle.Unregister(context.Background()); removeErr != nil {
		t.Fatal(removeErr)
	}
	var problem *subagent.Error
	if commitErr := setup.Commit(); !errors.As(commitErr, &problem) ||
		problem.Code != subagent.ErrorActivationSetupRevoked {
		t.Fatalf("Commit error = %v", commitErr)
	}
	if disposals != 1 {
		t.Fatalf("installation disposed %d times", disposals)
	}
	if closeErr := setup.Dispose(context.Background()); closeErr != nil {
		t.Fatal(closeErr)
	}
	if disposals != 1 {
		t.Fatalf("installation disposed %d times after child close", disposals)
	}
}

func TestContributionCanRemoveItselfDuringInstall(t *testing.T) {
	owner := New()
	var handle subagent.SetupRegistration
	disposals := 0
	registered, registerErr := owner.Register(
		setupRecord{
			install: func() (subagent.Installation, error) {
				if removeErr := handle.Unregister(context.Background()); removeErr != nil {
					return nil, removeErr
				}
				return installationRecord{
					dispose: func() error {
						disposals++
						return nil
					},
				}, nil
			},
		},
	)
	if registerErr != nil {
		t.Fatal(registerErr)
	}
	handle = registered
	setup := owner.Compose(Input{})
	if prepareErr := setup.Prepare(context.Background(), scopeRecord{}); prepareErr != nil {
		t.Fatal(prepareErr)
	}
	if commitErr := setup.Commit(); commitErr == nil {
		t.Fatal("self-revoked Setup committed")
	}
	if disposals != 1 {
		t.Fatalf("escaped installation disposed %d times", disposals)
	}
}

var _ agent.Scope = scopeRecord{}
var _ subagent.Setup = setupRecord{}
var _ subagent.Installation = installationRecord{}
