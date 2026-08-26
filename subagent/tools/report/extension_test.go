package report

import (
	"context"
	"errors"
	"testing"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/agent/scopedplugin"
	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/systemprompt"
	"github.com/gorenx/goren/tools"
)

type extensionScopeRecord struct {
	mounted plugin.Plugin
	effect  *extensionEffectRecord
	err     error
}

func (record *extensionScopeRecord) Agent() agent.Agent {
	return nil
}

func (record *extensionScopeRecord) MountPlugin(
	_ context.Context,
	instance plugin.Plugin,
) (agent.ScopeResource, error) {
	record.mounted = instance
	if record.err != nil {
		return nil, record.err
	}
	if record.effect == nil {
		record.effect = &extensionEffectRecord{}
	}
	return record.effect, nil
}

func (*extensionScopeRecord) Own(agent.ScopeResource) error {
	return nil
}

type extensionEffectRecord struct {
	disposals int
}

func (record *extensionEffectRecord) Dispose(context.Context) error {
	record.disposals++
	return nil
}

func TestExtensionMountsChildPluginInExecutionScope(t *testing.T) {
	scope := &extensionScopeRecord{}
	contribution := &extension{}
	installed, installErr := contribution.Install(
		context.Background(),
		scope,
	)
	if installErr != nil {
		t.Fatal(installErr)
	}
	child, matches := scope.mounted.(*childPlugin)
	if !matches {
		t.Fatalf("mounted Plugin = %T, want childPlugin", scope.mounted)
	}
	declaration := child.Manifest()
	wanted := map[string]bool{
		plugin.ServiceOf[reporter]().Name():                    false,
		plugin.ServiceOf[tools.ToolCatalog]().Name():           false,
		plugin.ServiceOf[systemprompt.PromptRegistry]().Name(): false,
	}
	for _, dependency := range declaration.Requires {
		if _, found := wanted[dependency.Name()]; found {
			wanted[dependency.Name()] = true
		}
	}
	for name, found := range wanted {
		if !found {
			t.Fatalf("child Plugin did not require %s", name)
		}
	}
	if uninstallErr := installed.Uninstall(context.Background()); uninstallErr != nil {
		t.Fatal(uninstallErr)
	}
	if uninstallErr := installed.Uninstall(context.Background()); uninstallErr != nil {
		t.Fatal(uninstallErr)
	}
	if scope.effect.disposals != 1 {
		t.Fatalf("mounted child disposed %d times, want 1", scope.effect.disposals)
	}
}

func TestExtensionReturnsChildMountFailure(t *testing.T) {
	sentinel := errors.New("mount failed")
	scope := &extensionScopeRecord{
		err: sentinel,
	}
	contribution := &extension{}
	installed, installErr := contribution.Install(
		context.Background(),
		scope,
	)
	if installed != nil || !errors.Is(installErr, sentinel) {
		t.Fatalf("Install = (%v, %v), want nil and sentinel", installed, installErr)
	}
}

func TestReleasedChildDoesNotRequestNestedTopologyMutation(t *testing.T) {
	effect := &extensionEffectRecord{}
	installed := &installation{}
	installed.attach(effect)
	installed.release(nil)
	if uninstallErr := installed.Uninstall(context.Background()); uninstallErr != nil {
		t.Fatal(uninstallErr)
	}
	if effect.disposals != 0 {
		t.Fatalf("released child disposed through Scope effect %d times", effect.disposals)
	}
}

var _ agent.Scope = (*extensionScopeRecord)(nil)
var _ scopedplugin.Scope = (*extensionScopeRecord)(nil)
var _ agent.ScopeResource = (*extensionEffectRecord)(nil)
