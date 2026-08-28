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
	boundcontract "github.com/gorenx/goren/subagent/bound"
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
	factory := newProvisionerMaterializer(extensionRegistry)
	scopeProvisioner, err := factory.provisioner(
		boundDefinitionForProvisioner(
			t,
			"bound system prompt",
			[]string{
				"second",
				"first",
			},
			&tools.ToolRestriction{
				Deny: []string{},
			},
		),
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
		"mount:@goren/subagent/bound-system-prompt",
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
	factory := newProvisionerMaterializer(extensionregistry.New())
	scopeProvisioner, err := factory.provisioner(
		boundDefinitionForProvisioner(t, "prompt", []string{"missing"}, nil),
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
	factory := newProvisionerMaterializer(extensionRegistry)
	scopeProvisioner, err := factory.provisioner(
		boundDefinitionForProvisioner(t, "prompt", []string{"failing"}, nil),
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
		"mount:@goren/subagent/bound-system-prompt",
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
	extensionIndex *extensionregistry.Registry,
	extensionNameValue string,
	order *[]string,
	options ...subagent.ExtensionOption,
) {
	t.Helper()
	registerBoundProvisionerExtensionWithFailure(
		t,
		extensionIndex,
		extensionNameValue,
		order,
		nil,
		options...,
	)
}

func newProvisionerMaterializer(
	extensionIndex *extensionregistry.Registry,
) *materializer {
	return &materializer{
		commonExtensions: extensionregistry.NewProvisioner(extensionIndex),
		extensions: extensionsStub{
			provision: func(names []string) (agent.Provisioner, error) {
				return extensionregistry.NewSelectedProvisioner(
					extensionIndex,
					names,
				)
			},
		},
	}
}

func boundDefinitionForProvisioner(
	testingContext *testing.T,
	systemPrompt string,
	selectedExtensions []string,
	restriction *tools.ToolRestriction,
) boundcontract.Definition {
	testingContext.Helper()
	definitionValue, err := boundcontract.NewDefinition(
		boundcontract.Draft{
			Name:            "researcher",
			Enabled:         true,
			SystemPrompt:    systemPrompt,
			ToolRestriction: restriction,
			Extensions:      selectedExtensions,
		},
		1,
	)
	if err != nil {
		testingContext.Fatal(err)
	}
	return definitionValue
}

func registerBoundProvisionerExtensionWithFailure(
	t *testing.T,
	extensionIndex *extensionregistry.Registry,
	extensionNameValue string,
	order *[]string,
	installErr error,
	options ...subagent.ExtensionOption,
) {
	t.Helper()
	_, err := extensionIndex.RegisterExtension(
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
