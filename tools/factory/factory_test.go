package factory_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/systemprompt"
	"github.com/gorenx/goren/tools"
	toolsfactory "github.com/gorenx/goren/tools/factory"
)

func TestFactoryCreatesToolsPlugin(t *testing.T) {
	t.Parallel()
	builder := toolsfactory.New()
	created, err := builder.Create(
		context.Background(),
		json.RawMessage(`{"maxParallelSubCalls":4}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, matches := created.(tools.ToolRuntime); !matches {
		t.Fatal("created Plugin does not implement ToolRuntime")
	}
	if _, matches := created.(tools.ToolCatalog); !matches {
		t.Fatal("created Plugin does not implement ToolCatalog")
	}
	if _, matches := created.(tools.PolicyRegistry); !matches {
		t.Fatal("created Plugin does not implement PolicyRegistry")
	}
	promptSettings, err := systemprompt.ValidateConfig(systemprompt.Config{})
	if err != nil {
		t.Fatal(err)
	}
	runtimeEngine := plugin.NewRuntime(plugin.RuntimeSettings{})
	if _, err := runtimeEngine.Start(
		context.Background(),
		systemprompt.New(
			promptSettings,
			systemprompt.RegistryOptions{},
		),
		created,
	); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := runtimeEngine.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	})
}

func TestFactoryRejectsInvalidConfiguration(t *testing.T) {
	t.Parallel()
	builder := toolsfactory.New()
	for _, rawConfig := range []string{
		`{"unknown":true}`,
		`{"mode":null}`,
		`{"mode":"code"}`,
		`{"maxParallelSubCalls":0}`,
		`[]`,
	} {
		if _, err := builder.Create(
			context.Background(),
			json.RawMessage(rawConfig),
		); err == nil {
			t.Fatalf("configuration %s succeeded", rawConfig)
		}
	}
}
