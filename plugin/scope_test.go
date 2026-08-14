package plugin_test

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/gorenx/goren/plugin"
)

var childScopeService = plugin.DefineService[string]("fixture.child-scope-service")

func TestChildScopeOwnsNestedEffectsAndSupportsEarlyDisposal(t *testing.T) {
	t.Parallel()
	requestContext := context.Background()
	engine := plugin.NewRuntime()
	calls := []string{}
	var childRelease plugin.Disposer
	var childScope *plugin.Scope
	if _, err := engine.Load(requestContext, fixturePlugin{
		metadata: plugin.Manifest{Name: "child-scope-lifecycle", Provides: []plugin.ServiceRef{childScopeService.Ref()}},
		body: func(_ context.Context, pluginScope *plugin.Scope) error {
			if _, provideErr := plugin.Provide(pluginScope, childScopeService, "root"); provideErr != nil {
				return provideErr
			}
			var childErr error
			childScope, childRelease, childErr = pluginScope.Child("agent")
			if childErr != nil {
				return childErr
			}
			if _, provideErr := plugin.Provide(childScope, childScopeService, "child"); provideErr == nil ||
				!strings.Contains(provideErr.Error(), "child scopes cannot provide") {
				return fmt.Errorf("child Provide error = %v", provideErr)
			}
			if _, ownErr := plugin.Own(childScope, "child-effect", func(context.Context) error {
				calls = append(calls, "child")
				return nil
			}); ownErr != nil {
				return ownErr
			}
			nestedScope, _, nestedErr := childScope.Child("turn")
			if nestedErr != nil {
				return nestedErr
			}
			_, ownErr := plugin.Own(nestedScope, "nested-effect", func(context.Context) error {
				calls = append(calls, "nested")
				return nil
			})
			return ownErr
		},
	}); err != nil {
		t.Fatal(err)
	}
	if childScope.Target().IsGlobal() {
		t.Fatal("child scope received the global target")
	}
	if err := childRelease(requestContext); err != nil {
		t.Fatal(err)
	}
	if err := childRelease(requestContext); err != nil {
		t.Fatalf("second child release = %v", err)
	}
	if want := []string{"nested", "child"}; !reflect.DeepEqual(calls, want) {
		t.Fatalf("cleanup order = %#v, want %#v", calls, want)
	}
}
