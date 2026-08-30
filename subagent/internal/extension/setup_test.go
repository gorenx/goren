package extension

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/subagent"
)

type extensionRecord struct {
	name  string
	order *[]string
}

func (extension extensionRecord) Apply(
	context.Context,
	agent.Agent,
	agent.ScopeEditor,
) error {
	*extension.order = append(*extension.order, extension.name)
	return nil
}

type setupEditor struct {
	agent.ScopeEditor
	resources []*resourceRecord
	owned     []agent.ScopeResource
	checks    []agent.ScopeCheck
}

func (editor *setupEditor) ApplyNestedSetup(
	requestContext context.Context,
	nestedSetup agent.Setup,
) (agent.ScopeResources, error) {
	if err := nestedSetup.Apply(requestContext, nil, editor); err != nil {
		return nil, err
	}
	resource := &resourceRecord{}
	editor.resources = append(editor.resources, resource)
	return resource, nil
}

func (editor *setupEditor) Own(resource agent.ScopeResource) error {
	editor.owned = append(editor.owned, resource)
	return nil
}

func (editor *setupEditor) Check(validation agent.ScopeCheck) error {
	editor.checks = append(editor.checks, validation)
	return nil
}

type resourceRecord struct {
	closed int
}

func (resource *resourceRecord) Close(context.Context) error {
	resource.closed++
	return nil
}

func TestCommonSetupAppliesExtensionsInRegistrationOrder(t *testing.T) {
	t.Parallel()
	owner := New()
	order := []string{}
	for _, name := range []string{"first", "second"} {
		if _, err := owner.RegisterExtension(extensionRecord{
			name:  name,
			order: &order,
		}); err != nil {
			t.Fatal(err)
		}
	}
	editor := &setupEditor{}
	if err := NewSetup(owner).Apply(context.Background(), nil, editor); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(order, []string{"first", "second"}) {
		t.Fatalf("Extension order = %v", order)
	}
	if len(editor.checks) != 1 || editor.checks[0].Check() != nil {
		t.Fatal("Extension Setup commit check is invalid")
	}
}

func TestSelectedSetupValidatesNamesBeforeMutation(t *testing.T) {
	t.Parallel()
	owner := New()
	order := []string{}
	if _, err := owner.RegisterExtension(
		extensionRecord{
			name:  "selected",
			order: &order,
		},
		subagent.WithExtensionName("selected"),
	); err != nil {
		t.Fatal(err)
	}
	configured, err := NewSelectedSetup(owner, []string{"missing"})
	if configured != nil || err == nil {
		t.Fatalf("NewSelectedSetup = (%v, %v)", configured, err)
	}
	if len(order) != 0 {
		t.Fatalf("validation applied Extensions: %v", order)
	}
}

func TestUnregisterClosesResidentExtensionResources(t *testing.T) {
	t.Parallel()
	owner := New()
	order := []string{}
	handle, err := owner.RegisterExtension(extensionRecord{
		name:  "resident",
		order: &order,
	})
	if err != nil {
		t.Fatal(err)
	}
	editor := &setupEditor{}
	if err = NewSetup(owner).Apply(context.Background(), nil, editor); err != nil {
		t.Fatal(err)
	}
	if err = handle.Unregister(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(editor.resources) != 1 || editor.resources[0].closed != 1 {
		t.Fatalf("resource close state = %#v", editor.resources)
	}
	if len(editor.checks) != 1 {
		t.Fatalf("checks = %d", len(editor.checks))
	}
	var revoked *subagent.Error
	if checkErr := editor.checks[0].Check(); !errors.As(checkErr, &revoked) ||
		revoked.Code != subagent.ErrorExtensionRevoked {
		t.Fatalf("check error = %v", checkErr)
	}
}

func TestRegistrationNameIsUnique(t *testing.T) {
	t.Parallel()
	owner := New()
	order := []string{}
	if _, err := owner.RegisterExtension(
		extensionRecord{
			name:  "first",
			order: &order,
		},
		subagent.WithExtensionName("named"),
	); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.RegisterExtension(
		extensionRecord{
			name:  "second",
			order: &order,
		},
		subagent.WithExtensionName("named"),
	); err == nil {
		t.Fatal("duplicate Extension name was accepted")
	}
}

var _ subagent.Extension = extensionRecord{}
var _ agent.ScopeEditor = (*setupEditor)(nil)
var _ agent.ScopeResources = (*resourceRecord)(nil)
