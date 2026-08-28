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
	wanted := []string{"first", "second", "undo-second", "undo-first"}
	if !reflect.DeepEqual(order, wanted) {
		t.Fatalf("Extension order = %v, want %v", order, wanted)
	}
}

func TestRegistryInstallsSelectedExtensionsInConfigOrder(t *testing.T) {
	owner := New()
	order := make([]string, 0)
	register := func(name string, options ...subagent.ExtensionOption) {
		t.Helper()
		_, err := owner.RegisterExtension(
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
			options...,
		)
		if err != nil {
			t.Fatal(err)
		}
	}
	register("common")
	register("first", subagent.WithExtensionName("first"))
	register("second", subagent.WithExtensionName("second"))

	configured, err := NewSelectedProvisioner(
		owner,
		[]string{
			"second",
			"first",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	acquired, err := configured.Provision(context.Background(), scopeRecord{})
	if err != nil {
		t.Fatal(err)
	}
	if err = acquired.Commit(); err != nil {
		t.Fatal(err)
	}
	if err = acquired.Dispose(context.Background()); err != nil {
		t.Fatal(err)
	}
	wanted := []string{
		"second",
		"first",
		"undo-first",
		"undo-second",
	}
	if !reflect.DeepEqual(order, wanted) {
		t.Fatalf("Extension order = %v, want %v", order, wanted)
	}
}

func TestNamedExtensionsAreNotInstalledWithoutSelection(t *testing.T) {
	owner := New()
	installed := 0
	if _, err := owner.RegisterExtension(
		extensionRecord{
			install: func() (subagent.ExtensionInstallation, error) {
				installed++
				return installationRecord{
					dispose: func() error { return nil },
				}, nil
			},
		},
		subagent.WithExtensionName("selected"),
	); err != nil {
		t.Fatal(err)
	}
	acquired, err := NewProvisioner(owner).Provision(
		context.Background(),
		scopeRecord{},
	)
	if err != nil || acquired != nil || installed != 0 {
		t.Fatalf(
			"Provision = (%v, %v), installs = %d, want nil, nil, 0",
			acquired,
			err,
			installed,
		)
	}
}

func TestExtensionDirectoryListsOnlyNamedRegistrations(t *testing.T) {
	owner := New()
	registered := extensionRecord{
		install: func() (subagent.ExtensionInstallation, error) {
			return installationRecord{
				dispose: func() error { return nil },
			}, nil
		},
	}
	if _, err := owner.RegisterExtension(registered); err != nil {
		t.Fatal(err)
	}
	first, err := owner.RegisterExtension(
		registered,
		subagent.WithExtensionName("first"),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = owner.RegisterExtension(
		registered,
		subagent.WithExtensionName("second"),
	); err != nil {
		t.Fatal(err)
	}
	wanted := []subagent.ExtensionDescriptor{
		{
			Name: "first",
		},
		{
			Name: "second",
		},
	}
	if got := owner.ListExtensions(); !reflect.DeepEqual(got, wanted) {
		t.Fatalf("Extension directory = %#v, want %#v", got, wanted)
	}
	if err = first.Unregister(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := owner.ListExtensions(); !reflect.DeepEqual(
		got,
		wanted[1:],
	) {
		t.Fatalf("Extension directory after removal = %#v", got)
	}
}

func TestSelectedExtensionValidationPrecedesInstallation(t *testing.T) {
	tests := []struct {
		name     string
		selected []string
	}{
		{
			name:     "unknown",
			selected: []string{"missing"},
		},
		{
			name:     "duplicate",
			selected: []string{"selected", "selected"},
		},
		{
			name:     "blank",
			selected: []string{" "},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			owner := New()
			installed := 0
			register := func(options ...subagent.ExtensionOption) {
				t.Helper()
				_, err := owner.RegisterExtension(
					extensionRecord{
						install: func() (subagent.ExtensionInstallation, error) {
							installed++
							return installationRecord{
								dispose: func() error { return nil },
							}, nil
						},
					},
					options...,
				)
				if err != nil {
					t.Fatal(err)
				}
			}
			register()
			register(subagent.WithExtensionName("selected"))
			configured, err := NewSelectedProvisioner(
				owner,
				test.selected,
			)
			if err == nil || configured != nil || installed != 0 {
				t.Fatalf(
					"NewSelectedProvisioner = (%v, %v), installs = %d, want nil, error, 0",
					configured,
					err,
					installed,
				)
			}
		})
	}
}

func TestSelectedExtensionRemovedAfterResolutionFailsProvision(t *testing.T) {
	owner := New()
	handle, err := owner.RegisterExtension(
		extensionRecord{
			install: func() (subagent.ExtensionInstallation, error) {
				return installationRecord{
					dispose: func() error { return nil },
				}, nil
			},
		},
		subagent.WithExtensionName("selected"),
	)
	if err != nil {
		t.Fatal(err)
	}
	configured, err := NewSelectedProvisioner(
		owner,
		[]string{"selected"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err = handle.Unregister(context.Background()); err != nil {
		t.Fatal(err)
	}
	acquired, err := configured.Provision(
		context.Background(),
		scopeRecord{},
	)
	var problem *subagent.Error
	if acquired != nil || !errors.As(err, &problem) ||
		problem.Code != subagent.ErrorUnknownExtension {
		t.Fatalf("Provision = (%v, %v), want unknown Extension", acquired, err)
	}
}

func TestNamedExtensionRegistrationValidation(t *testing.T) {
	owner := New()
	registered := extensionRecord{
		install: func() (subagent.ExtensionInstallation, error) {
			return installationRecord{
				dispose: func() error { return nil },
			}, nil
		},
	}
	if _, err := owner.RegisterExtension(
		registered,
		subagent.WithExtensionName("selected"),
	); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name    string
		options []subagent.ExtensionOption
	}{
		{
			name: "duplicate registration",
			options: []subagent.ExtensionOption{
				subagent.WithExtensionName("selected"),
			},
		},
		{
			name: "blank",
			options: []subagent.ExtensionOption{
				subagent.WithExtensionName(" "),
			},
		},
		{
			name: "untrimmed",
			options: []subagent.ExtensionOption{
				subagent.WithExtensionName(" selected-two"),
			},
		},
		{
			name: "multiple",
			options: []subagent.ExtensionOption{
				subagent.WithExtensionName("one"),
				subagent.WithExtensionName("two"),
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := owner.RegisterExtension(
				registered,
				test.options...,
			); err == nil {
				t.Fatal("invalid named Extension registration succeeded")
			}
		})
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
