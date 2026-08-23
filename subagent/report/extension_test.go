package report

import (
	"context"
	"errors"
	"testing"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/subagent"
	"github.com/gorenx/goren/systemprompt"
	"github.com/gorenx/goren/tools"
)

type activationScopeRecord struct {
	mounted plugin.Plugin
	effect  *activationEffectRecord
	err     error
}

func (record *activationScopeRecord) Agent() agent.Agent {
	return nil
}

func (record *activationScopeRecord) Mount(
	_ context.Context,
	instance plugin.Plugin,
) (agent.Effect, error) {
	record.mounted = instance
	if record.err != nil {
		return nil, record.err
	}
	if record.effect == nil {
		record.effect = &activationEffectRecord{}
	}
	return record.effect, nil
}

func (*activationScopeRecord) Own(agent.Effect) error {
	return nil
}

type activationEffectRecord struct {
	disposals int
}

func (record *activationEffectRecord) Dispose(context.Context) error {
	record.disposals++
	return nil
}

func TestExtensionMountsChildPluginInActivationScope(t *testing.T) {
	scope := &activationScopeRecord{}
	contribution := &extension{}
	installed, installErr := contribution.Install(
		context.Background(),
		subagent.ActivationContext{
			Scope: scope,
		},
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
	scope := &activationScopeRecord{
		err: sentinel,
	}
	contribution := &extension{}
	installed, installErr := contribution.Install(
		context.Background(),
		subagent.ActivationContext{
			Scope: scope,
		},
	)
	if installed != nil || !errors.Is(installErr, sentinel) {
		t.Fatalf("Install = (%v, %v), want nil and sentinel", installed, installErr)
	}
}

func TestReleasedChildDoesNotRequestNestedTopologyMutation(t *testing.T) {
	effect := &activationEffectRecord{}
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

var _ agent.Scope = (*activationScopeRecord)(nil)
var _ agent.Effect = (*activationEffectRecord)(nil)
