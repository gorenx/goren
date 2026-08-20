package factory_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/gorenx/goren/llm"
	llmfactory "github.com/gorenx/goren/llm/factory"
	"github.com/gorenx/goren/plugin"
)

type consumerPlugin struct {
	plugin.Base
	models llm.LlmRuntime
}

func (*consumerPlugin) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: "llm-consumer",
		Requires: []plugin.ServiceType{
			plugin.ServiceOf[llm.LlmRuntime](),
		},
	}
}

func (consumer *consumerPlugin) Apply(context.Context) error {
	models, err := plugin.Require[llm.LlmRuntime](consumer)
	if err != nil {
		return err
	}
	consumer.models = models
	return nil
}

func (consumer *consumerPlugin) Dispose(context.Context) error {
	consumer.models = nil
	return nil
}

func TestFactoryCreatesLLMRuntimePlugin(t *testing.T) {
	t.Parallel()
	builder := llmfactory.New(nil)
	instance, err := builder.Create(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	consumer := &consumerPlugin{}
	runtimeEngine := plugin.NewRuntime(plugin.RuntimeSettings{})
	if _, err := runtimeEngine.Start(context.Background(), consumer, instance); err != nil {
		t.Fatal(err)
	}
	defer runtimeEngine.Shutdown(context.Background())
	if consumer.models == nil {
		t.Fatal("LLM Runtime was not resolved")
	}
}

func TestFactoryRejectsUnknownConfiguration(t *testing.T) {
	t.Parallel()
	builder := llmfactory.New(nil)
	for _, testCase := range []struct {
		name        string
		rawConfig   json.RawMessage
		wantMessage string
	}{
		{
			name:        "unknown field",
			rawConfig:   json.RawMessage(`{"unknown":true}`),
			wantMessage: "unknown field",
		},
		{
			name:        "null",
			rawConfig:   json.RawMessage(`null`),
			wantMessage: "JSON object",
		},
	} {
		selectedCase := testCase
		t.Run(selectedCase.name, func(t *testing.T) {
			t.Parallel()
			if _, err := builder.Create(
				context.Background(),
				selectedCase.rawConfig,
			); err == nil || !strings.Contains(err.Error(), selectedCase.wantMessage) {
				t.Fatalf("Create error = %v", err)
			}
		})
	}
}
