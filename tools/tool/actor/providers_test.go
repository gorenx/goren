package toolactor_test

import (
	"testing"

	"github.com/gorenx/goren/toolaskuser"
	"github.com/gorenx/goren/tools/tool"
	toolactor "github.com/gorenx/goren/tools/tool/actor"
)

func TestProvidersInstallOnceAndRejectPartialBindings(t *testing.T) {
	bindings := &toolactor.Providers{}
	err := bindings.Install(tool.Providers{
		toolaskuser.Name: &provider{},
		"missing":        nil,
	})
	if err == nil {
		t.Fatal("invalid partial table was installed")
	}
	entries := tool.Providers{
		toolaskuser.Name: &provider{},
	}
	err = bindings.Install(entries)
	if err != nil {
		t.Fatal(err)
	}
	delete(entries, toolaskuser.Name)
	err = bindings.Install(nil)
	if err == nil {
		t.Fatal("installed bindings were replaced")
	}
	empty, err := toolactor.NewProviders(nil)
	if err != nil {
		t.Fatal(err)
	}
	err = empty.Install(entries)
	if err == nil {
		t.Fatal("an installed empty table was treated as uninitialized")
	}
}
