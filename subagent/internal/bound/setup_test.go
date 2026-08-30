package bound

import (
	"context"
	"reflect"
	"testing"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/subagent"
	boundcontract "github.com/gorenx/goren/subagent/bound"
	extensionregistry "github.com/gorenx/goren/subagent/internal/extension"
	"github.com/gorenx/goren/systemprompt"
	"github.com/gorenx/goren/tools"
)

type boundSetupEditor struct {
	agent.ScopeEditor
	order *[]string
}

func (editor *boundSetupEditor) ApplyNestedSetup(
	requestContext context.Context,
	nestedSetup agent.Setup,
) (agent.ScopeResources, error) {
	if err := nestedSetup.Apply(requestContext, nil, editor); err != nil {
		return nil, err
	}
	return emptyBoundResources{}, nil
}

func (editor *boundSetupEditor) AddPromptSection(
	_ context.Context,
	section systemprompt.PromptSection,
) error {
	*editor.order = append(*editor.order, "prompt:"+section.Name)
	return nil
}

func (editor *boundSetupEditor) AddToolRestriction(
	_ context.Context,
	name string,
	_ tools.ToolRestriction,
) error {
	*editor.order = append(*editor.order, "restriction:"+name)
	return nil
}

func (*boundSetupEditor) Own(agent.ScopeResource) error { return nil }
func (*boundSetupEditor) Check(agent.ScopeCheck) error  { return nil }

type emptyBoundResources struct{}

func (emptyBoundResources) Close(context.Context) error { return nil }

type boundExtension struct {
	name  string
	order *[]string
}

func (extension boundExtension) Apply(
	context.Context,
	agent.Agent,
	agent.ScopeEditor,
) error {
	*extension.order = append(*extension.order, "extension:"+extension.name)
	return nil
}

func TestBuildBoundSetupAppliesPolicyCommonAndSelectedExtensions(t *testing.T) {
	t.Parallel()
	order := []string{}
	extensionRegistry := extensionregistry.New()
	registerBoundExtension(t, extensionRegistry, "common", &order)
	registerBoundExtension(
		t,
		extensionRegistry,
		"first",
		&order,
		subagent.WithExtensionName("first"),
	)
	registerBoundExtension(
		t,
		extensionRegistry,
		"second",
		&order,
		subagent.WithExtensionName("second"),
	)
	factory := newSetupMaterializer(extensionRegistry)
	configured, err := factory.setup(
		boundDefinitionForSetup(
			t,
			[]string{"second", "first"},
		),
		false,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err = configured.Apply(
		context.Background(),
		newBoundAgent(t, "bound-setup"),
		&boundSetupEditor{
			order: &order,
		},
	); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"prompt:subagent:bound",
		"restriction:subagent",
		"extension:common",
		"extension:second",
		"extension:first",
	}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("Setup order = %v, want %v", order, want)
	}
}

func TestBuildBoundSetupRejectsUnknownSelectionBeforeApply(t *testing.T) {
	t.Parallel()
	factory := newSetupMaterializer(extensionregistry.New())
	configured, err := factory.setup(
		boundDefinitionForSetup(t, []string{"missing"}),
		false,
	)
	if err == nil || configured != nil {
		t.Fatalf("setup = (%v, %v), want nil, error", configured, err)
	}
}

func newSetupMaterializer(extensionRegistry *extensionregistry.Registry) *materializer {
	return &materializer{
		commonExtensions: extensionregistry.NewSetup(extensionRegistry),
		extensions: extensionsStub{
			setup: func(names []string) (agent.Setup, error) {
				return extensionregistry.NewSelectedSetup(extensionRegistry, names)
			},
		},
	}
}

func boundDefinitionForSetup(
	t *testing.T,
	selected []string,
) boundcontract.Definition {
	t.Helper()
	restriction := tools.ToolRestriction{
		Deny: []string{},
	}
	definition, err := boundcontract.NewDefinition(
		boundcontract.Draft{
			Name:            "researcher",
			Enabled:         true,
			SystemPrompt:    "bound prompt",
			ToolRestriction: &restriction,
			Extensions:      selected,
		},
		1,
	)
	if err != nil {
		t.Fatal(err)
	}
	return definition
}

func registerBoundExtension(
	t *testing.T,
	owner *extensionregistry.Registry,
	name string,
	order *[]string,
	options ...subagent.ExtensionOption,
) {
	t.Helper()
	if _, err := owner.RegisterExtension(
		boundExtension{
			name:  name,
			order: order,
		},
		options...,
	); err != nil {
		t.Fatal(err)
	}
}

var _ agent.ScopeEditor = (*boundSetupEditor)(nil)
var _ subagent.Extension = boundExtension{}
