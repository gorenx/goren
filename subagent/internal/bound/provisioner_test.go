package bound

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/agent/scopedplugin"
	pluginruntime "github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/subagent"
	extensionregistry "github.com/gorenx/goren/subagent/internal/extension"
	"github.com/gorenx/goren/tools"
)

type boundProvisionerScope struct {
	order *[]string
}

func (*boundProvisionerScope) Agent() agent.Agent { return nil }

func (*boundProvisionerScope) Own(agent.ScopeResource) error { return nil }

func (scope *boundProvisionerScope) MountPlugin(
	_ context.Context,
	instance pluginruntime.Plugin,
) (agent.ScopeResource, error) {
	pluginName := instance.Manifest().Name
	*scope.order = append(*scope.order, "mount:"+pluginName)
	return &boundProvisionerResource{}, nil
}

type boundProvisionerResource struct{}

func (*boundProvisionerResource) Dispose(context.Context) error { return nil }

type boundProvisionerExtension struct {
	name    string
	order   *[]string
	install func() error
}

func (extension boundProvisionerExtension) Install(
	context.Context,
	agent.Scope,
) (subagent.ExtensionInstallation, error) {
	*extension.order = append(*extension.order, "install:"+extension.name)
	if extension.install != nil {
		if err := extension.install(); err != nil {
			return nil, err
		}
	}
	return &boundProvisionerInstallation{
		name:  extension.name,
		order: extension.order,
	}, nil
}

type boundProvisionerInstallation struct {
	name        string
	order       *[]string
	uninstalled bool
}

func (installation *boundProvisionerInstallation) Uninstall(
	context.Context,
) error {
	if installation.uninstalled {
		return nil
	}
	installation.uninstalled = true
	*installation.order = append(
		*installation.order,
		"uninstall:"+installation.name,
	)
	return nil
}

func TestBuildBoundProvisionerInstallsPoliciesCommonAndSelectedExtensions(
	t *testing.T,
) {
	order := make([]string, 0)
	extensionRegistry := extensionregistry.New()
	registerBoundProvisionerExtension(t, extensionRegistry, "common", &order)
	registerBoundProvisionerExtension(
		t,
		extensionRegistry,
		"first",
		&order,
		subagent.WithExtensionName("first"),
	)
	registerBoundProvisionerExtension(
		t,
		extensionRegistry,
		"second",
		&order,
		subagent.WithExtensionName("second"),
	)
	persona := "bound persona"
	owner := newProvisionerService(t, extensionRegistry)
	scopeProvisioner, err := owner.materializer.provisioner(
		subagent.BoundConfigSnapshot{
			Persona: &persona,
			ToolRestriction: &tools.ToolRestriction{
				Deny: []string{},
			},
			Extensions: []string{
				"second",
				"first",
			},
		},
		false,
	)
	if err != nil {
		t.Fatal(err)
	}
	acquired, err := scopeProvisioner.Provision(
		context.Background(),
		&boundProvisionerScope{
			order: &order,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err = acquired.Commit(); err != nil {
		t.Fatal(err)
	}
	wantInitial := []string{
		"mount:@goren/subagent/persona",
		"mount:@goren/subagent/tool-restriction",
		"install:common",
		"install:second",
		"install:first",
	}
	if !reflect.DeepEqual(order, wantInitial) {
		t.Fatalf("provision order = %v, want %v", order, wantInitial)
	}
	if err = acquired.Dispose(context.Background()); err != nil {
		t.Fatal(err)
	}
	wantClosed := append(
		wantInitial,
		"uninstall:first",
		"uninstall:second",
		"uninstall:common",
	)
	if !reflect.DeepEqual(order, wantClosed) {
		t.Fatalf("dispose order = %v, want %v", order, wantClosed)
	}
}

func TestBuildBoundProvisionerRejectsUnknownSelectionBeforeScopeMutation(
	t *testing.T,
) {
	owner := newProvisionerService(t, extensionregistry.New())
	scopeProvisioner, err := owner.materializer.provisioner(
		subagent.BoundConfigSnapshot{
			Extensions: []string{"missing"},
		},
		false,
	)
	if err == nil || scopeProvisioner != nil {
		t.Fatalf(
			"provisioner = (%v, %v), want nil, error",
			scopeProvisioner,
			err,
		)
	}
}

func TestBoundProvisionerRollsBackCommonWhenSelectedInstallFails(t *testing.T) {
	order := make([]string, 0)
	extensionRegistry := extensionregistry.New()
	registerBoundProvisionerExtension(t, extensionRegistry, "common", &order)
	sentinel := errors.New("selected install failed")
	registerBoundProvisionerExtensionWithFailure(
		t,
		extensionRegistry,
		"failing",
		&order,
		sentinel,
		subagent.WithExtensionName("failing"),
	)
	owner := newProvisionerService(t, extensionRegistry)
	scopeProvisioner, err := owner.materializer.provisioner(
		subagent.BoundConfigSnapshot{
			Extensions: []string{"failing"},
		},
		false,
	)
	if err != nil {
		t.Fatal(err)
	}
	acquired, err := scopeProvisioner.Provision(
		context.Background(),
		&boundProvisionerScope{
			order: &order,
		},
	)
	if acquired != nil || !errors.Is(err, sentinel) {
		t.Fatalf("Provision = (%v, %v), want nil, sentinel", acquired, err)
	}
	want := []string{
		"install:common",
		"install:failing",
		"uninstall:common",
	}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("rollback order = %v, want %v", order, want)
	}
}

func registerBoundProvisionerExtension(
	t *testing.T,
	registry *extensionregistry.Registry,
	extensionNameValue string,
	order *[]string,
	options ...subagent.ExtensionOption,
) {
	t.Helper()
	registerBoundProvisionerExtensionWithFailure(
		t,
		registry,
		extensionNameValue,
		order,
		nil,
		options...,
	)
}

func newProvisionerService(
	t *testing.T,
	registry *extensionregistry.Registry,
) *Service {
	t.Helper()
	owner, err := New(
		Dependencies{
			CommonExtensions: extensionregistry.NewProvisioner(registry),
			Extensions: boundExtensionsRecord{
				validate: registry.ValidateSelection,
				provision: func(
					names []string,
				) (agent.Provisioner, error) {
					return extensionregistry.NewSelectedProvisioner(
						registry,
						names,
					)
				},
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	return owner
}

func registerBoundProvisionerExtensionWithFailure(
	t *testing.T,
	registry *extensionregistry.Registry,
	extensionNameValue string,
	order *[]string,
	installErr error,
	options ...subagent.ExtensionOption,
) {
	t.Helper()
	_, err := registry.RegisterExtension(
		boundProvisionerExtension{
			name:  extensionNameValue,
			order: order,
			install: func() error {
				return installErr
			},
		},
		options...,
	)
	if err != nil {
		t.Fatal(err)
	}
}

var _ scopedplugin.Scope = (*boundProvisionerScope)(nil)
var _ subagent.Extension = boundProvisionerExtension{}
var _ subagent.ExtensionInstallation = (*boundProvisionerInstallation)(nil)
