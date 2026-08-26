package extension

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/subagent"
)

type scopeRecord struct{}

func (scopeRecord) Agent() agent.Agent { return nil }

func (scopeRecord) Own(agent.ScopeResource) error { return nil }

type extensionRecord struct {
	install func() (subagent.ExtensionInstallation, error)
}

func (record extensionRecord) Install(
	context.Context,
	agent.Scope,
) (subagent.ExtensionInstallation, error) {
	return record.install()
}

type installationRecord struct {
	dispose func() error
}

func (record installationRecord) Uninstall(context.Context) error {
	return record.dispose()
}

func TestRegistryProvisionsInOrderAndReleasesWithChild(t *testing.T) {
	owner := New()
	order := make([]string, 0)
	for _, name := range []string{"first", "second"} {
		name := name
		if _, registerErr := owner.RegisterExtension(
			extensionRecord{
				install: func() (subagent.ExtensionInstallation, error) {
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
	configured := NewProvisioner(owner)
	acquired, provisionErr := configured.Provision(
		context.Background(),
		scopeRecord{},
	)
	if provisionErr != nil {
		t.Fatal(provisionErr)
	}
	if commitErr := acquired.Commit(); commitErr != nil {
		t.Fatal(commitErr)
	}
	if closeErr := acquired.Dispose(context.Background()); closeErr != nil {
		t.Fatal(closeErr)
	}
	wanted := []string{"first", "second", "undo-first", "undo-second"}
	if !reflect.DeepEqual(order, wanted) {
		t.Fatalf("Extension order = %v, want %v", order, wanted)
	}
}

func TestProvisionFailureRollsBackInstalledExtensions(t *testing.T) {
	owner := New()
	sentinel := errors.New("install failed")
	disposals := 0
	if _, registerErr := owner.RegisterExtension(
		extensionRecord{
			install: func() (subagent.ExtensionInstallation, error) {
				return installationRecord{
					dispose: func() error {
						disposals++
						return nil
					},
				}, nil
			},
		},
	); registerErr != nil {
		t.Fatal(registerErr)
	}
	if _, registerErr := owner.RegisterExtension(
		extensionRecord{
			install: func() (subagent.ExtensionInstallation, error) {
				return nil, sentinel
			},
		},
	); registerErr != nil {
		t.Fatal(registerErr)
	}
	configured := NewProvisioner(owner)
	acquired, provisionErr := configured.Provision(
		context.Background(),
		scopeRecord{},
	)
	if acquired != nil || !errors.Is(provisionErr, sentinel) {
		t.Fatalf("Provision = (%v, %v), want nil and sentinel", acquired, provisionErr)
	}
	if disposals != 1 {
		t.Fatalf("rollback disposed %d installations, want 1", disposals)
	}
}

func TestEmptyRegistryProducesNoProvisioning(t *testing.T) {
	configured := NewProvisioner(New())
	acquired, provisionErr := configured.Provision(
		context.Background(),
		scopeRecord{},
	)
	if provisionErr != nil || acquired != nil {
		t.Fatalf("Provision = (%v, %v), want nil, nil", acquired, provisionErr)
	}
}

func TestRegistrationRemovalInvalidatesUnpublishedProvisioning(t *testing.T) {
	owner := New()
	disposals := 0
	handle, registerErr := owner.RegisterExtension(
		extensionRecord{
			install: func() (subagent.ExtensionInstallation, error) {
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
	configured := NewProvisioner(owner)
	acquired, provisionErr := configured.Provision(
		context.Background(),
		scopeRecord{},
	)
	if provisionErr != nil {
		t.Fatal(provisionErr)
	}
	if removeErr := handle.Unregister(context.Background()); removeErr != nil {
		t.Fatal(removeErr)
	}
	var problem *subagent.Error
	if commitErr := acquired.Commit(); !errors.As(commitErr, &problem) ||
		problem.Code != subagent.ErrorExtensionRevoked {
		t.Fatalf("Commit error = %v", commitErr)
	}
	if disposals != 1 {
		t.Fatalf("installation disposed %d times", disposals)
	}
	if closeErr := acquired.Dispose(context.Background()); closeErr != nil {
		t.Fatal(closeErr)
	}
	if disposals != 1 {
		t.Fatalf("installation disposed %d times after child close", disposals)
	}
}

func TestExtensionCanRemoveItselfDuringInstall(t *testing.T) {
	owner := New()
	var handle subagent.ExtensionRegistration
	disposals := 0
	registered, registerErr := owner.RegisterExtension(
		extensionRecord{
			install: func() (subagent.ExtensionInstallation, error) {
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
	configured := NewProvisioner(owner)
	acquired, provisionErr := configured.Provision(
		context.Background(),
		scopeRecord{},
	)
	if provisionErr != nil {
		t.Fatal(provisionErr)
	}
	if commitErr := acquired.Commit(); commitErr == nil {
		t.Fatal("self-revoked Extension committed")
	}
	if disposals != 1 {
		t.Fatalf("escaped installation disposed %d times", disposals)
	}
}

func TestRegistrationRemovalAfterCommitRevokesResidentInstallation(t *testing.T) {
	owner := New()
	disposals := 0
	handle, registerErr := owner.RegisterExtension(
		extensionRecord{
			install: func() (subagent.ExtensionInstallation, error) {
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
	configured := NewProvisioner(owner)
	acquired, provisionErr := configured.Provision(
		context.Background(),
		scopeRecord{},
	)
	if provisionErr != nil {
		t.Fatal(provisionErr)
	}
	if commitErr := acquired.Commit(); commitErr != nil {
		t.Fatal(commitErr)
	}
	if removeErr := handle.Unregister(context.Background()); removeErr != nil {
		t.Fatal(removeErr)
	}
	if disposals != 1 {
		t.Fatalf("resident installation disposed %d times, want 1", disposals)
	}
	if secondCommitErr := acquired.Commit(); secondCommitErr != nil {
		t.Fatalf("committed Provisioning was invalidated: %v", secondCommitErr)
	}
	if closeErr := acquired.Dispose(context.Background()); closeErr != nil {
		t.Fatal(closeErr)
	}
	if disposals != 1 {
		t.Fatalf("resident installation disposed %d times after close", disposals)
	}
}

var _ agent.Scope = scopeRecord{}
var _ subagent.Extension = extensionRecord{}
var _ subagent.ExtensionInstallation = installationRecord{}
