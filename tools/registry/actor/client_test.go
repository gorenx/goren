package toolactor_test

import (
	"context"
	"github.com/gorenx/goren/llm"
	toolregistry "github.com/gorenx/goren/tools/registry"
	toolactor "github.com/gorenx/goren/tools/registry/actor"
	"testing"
	"time"
)

func TestClientReturnsUnmergedGlobalAndCustomSchemas(t *testing.T) {
	system, target := startRegistry(t, true)
	endpoint, err := toolactor.NewClient(system, target, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	reply := ask[toolactor.Completion](t, system, target, toolactor.RegisterSource{
		Name: "local",
		Catalog: toolregistry.Catalog{
			Schemas: []llm.ToolSchema{
				{
					Name:       "read",
					Parameters: []byte(`{"type":"object"}`),
				},
			},
		},
	})
	if reply.Err != nil {
		t.Fatal(reply.Err)
	}
	hidden := false
	err = endpoint.ConfigureSession(context.Background(), "agent", toolregistry.Catalog{
		InheritGlobal: true,
		Policies: map[string]toolregistry.Policy{
			"read": {
				Visible: &hidden,
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	configured, err := endpoint.Read(context.Background(), "agent")
	if err != nil || len(configured.Global.Schemas) != 1 || len(configured.Custom.Schemas) != 0 || *configured.Custom.Policies["read"].Visible {
		t.Fatalf("configuration=%+v error=%v", configured, err)
	}
}
