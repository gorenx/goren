package factory_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/systemprompt"
	systempromptfactory "github.com/gorenx/goren/systemprompt/factory"
)

func TestFactoryCreatesSystemPromptPlugin(t *testing.T) {
	t.Parallel()
	builder := systempromptfactory.New()
	created, err := builder.Create(
		context.Background(),
		json.RawMessage(`{"persona":"fixture"}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	prompts, matches := created.(systemprompt.Assembler)
	if !matches {
		t.Fatal("created Plugin does not implement Assembler")
	}
	if _, matches := created.(systemprompt.Contributions); !matches {
		t.Fatal("created Plugin does not implement Contributions")
	}
	runtimeEngine := plugin.NewRuntime(plugin.RuntimeSettings{})
	if _, err := runtimeEngine.Start(context.Background(), created); err != nil {
		t.Fatal(err)
	}
	assembled, err := prompts.Assemble(
		context.Background(),
		systemprompt.AssembleContext{},
	)
	if err != nil {
		t.Fatal(err)
	}
	rendered, err := systemprompt.RenderPrompt(assembled)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rendered, "fixture") {
		t.Fatalf("rendered prompt = %q", rendered)
	}
}

func TestFactoryRejectsInvalidConfiguration(t *testing.T) {
	t.Parallel()
	builder := systempromptfactory.New()
	for _, rawConfig := range []string{
		`{"unknown":true}`,
		`{"includeHarnessIdentity":null}`,
		`{"toolOrder":[]}`,
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
