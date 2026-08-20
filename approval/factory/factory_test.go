package factory_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/gorenx/goren/approval"
	approvalfactory "github.com/gorenx/goren/approval/factory"
	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/systemprompt"
)

func TestFactoryCreatesApprovalPlugin(t *testing.T) {
	t.Parallel()
	builder := approvalfactory.New()
	created, err := builder.Create(
		context.Background(),
		json.RawMessage(`{"policy":"never"}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	approvalPlugin, matches := created.(approval.Approval)
	if !matches {
		t.Fatal("created Plugin does not implement Approval")
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
	conversation, err := session.New(
		"approval-factory",
		session.CreateOptions{},
	)
	if err != nil {
		t.Fatal(err)
	}
	selectedPolicy, err := approvalPlugin.EffectivePolicy(conversation)
	if err != nil {
		t.Fatal(err)
	}
	if selectedPolicy != approval.PolicyNever {
		t.Fatalf("policy = %q", selectedPolicy)
	}
}

func TestFactoryRejectsInvalidConfiguration(t *testing.T) {
	t.Parallel()
	builder := approvalfactory.New()
	for _, rawConfig := range []string{
		`{"unknown":true}`,
		`{"policy":null}`,
		`{"policy":"sometimes"}`,
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
