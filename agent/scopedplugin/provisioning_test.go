package scopedplugin_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/agent/scopedplugin"
	"github.com/gorenx/goren/plugin"
)

type plainScope struct{}

func (*plainScope) Agent() agent.Agent            { return nil }
func (*plainScope) Own(agent.ScopeResource) error { return nil }

type pluginScope struct {
	mounts int
	failAt int
	order  *[]string
}

func (*pluginScope) Agent() agent.Agent            { return nil }
func (*pluginScope) Own(agent.ScopeResource) error { return nil }

func (scope *pluginScope) MountPlugin(
	context.Context,
	plugin.Plugin,
) (agent.ScopeResource, error) {
	scope.mounts++
	if scope.mounts == scope.failAt {
		return nil, errors.New("mount failed")
	}
	return &mountedResource{
		name:  scope.mounts,
		order: scope.order,
	}, nil
}

type mountedResource struct {
	name  int
	order *[]string
}

func (resource *mountedResource) Dispose(context.Context) error {
	*resource.order = append(
		*resource.order,
		string(rune('0'+resource.name)),
	)
	return nil
}

type pluginRecord struct {
	plugin.Base
	name string
}

func (record *pluginRecord) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: record.name,
	}
}

func (*pluginRecord) Apply(context.Context) error   { return nil }
func (*pluginRecord) Dispose(context.Context) error { return nil }

func TestMountRejectsPluginNeutralScope(t *testing.T) {
	t.Parallel()
	_, err := scopedplugin.Mount(
		context.Background(),
		&plainScope{},
		&pluginRecord{
			name: "plain",
		},
	)
	if err == nil {
		t.Fatal("Plugin-neutral Agent Scope accepted Plugin mounting")
	}
}

func TestMountPluginsRollsBackPartialMountsInReverse(t *testing.T) {
	t.Parallel()
	order := make([]string, 0, 2)
	target := &pluginScope{
		failAt: 3,
		order:  &order,
	}
	_, err := scopedplugin.MountPlugins(
		&pluginRecord{
			name: "one",
		},
		&pluginRecord{
			name: "two",
		},
		&pluginRecord{
			name: "three",
		},
	).Provision(context.Background(), target)
	if err == nil || !reflect.DeepEqual(order, []string{"2", "1"}) {
		t.Fatalf("error=%v rollback order=%v", err, order)
	}
}

var _ agent.Scope = (*plainScope)(nil)
var _ scopedplugin.Scope = (*pluginScope)(nil)
var _ agent.ScopeResource = (*mountedResource)(nil)
var _ plugin.Plugin = (*pluginRecord)(nil)
