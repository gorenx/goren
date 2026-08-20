package deepseek_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gorenx/goren/credentials"
	"github.com/gorenx/goren/internal/llm/deepseek"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/plugin"
)

type platformFixture struct {
	values    map[string]string
	home      string
	homeCalls *atomic.Int32
}

func (deployment platformFixture) Lookup(variableName string) (string, bool) {
	value, found := deployment.values[variableName]
	return value, found
}

func (deployment platformFixture) UserHomeDir() (string, error) {
	if deployment.homeCalls != nil {
		deployment.homeCalls.Add(1)
	}
	return deployment.home, nil
}

type credentialStoreFixture struct{}

func (credentialStoreFixture) Load(context.Context, credentials.Ref) (string, bool, error) {
	return "", false, nil
}

func (credentialStoreFixture) Save(context.Context, credentials.Ref, string) error {
	return nil
}

func (credentialStoreFixture) Delete(context.Context, credentials.Ref) error {
	return nil
}

func (credentialStoreFixture) Source() string {
	return "fixture"
}

type conflictingAdapter struct{}

func (*conflictingAdapter) Stream(context.Context, llm.GenerateOptions) (llm.ChunkStream, error) {
	return nil, errors.New("not used")
}

func TestPluginOwnsProviderContributionsAndLazyIdentity(t *testing.T) {
	t.Parallel()
	var homeCalls atomic.Int32
	deployment := platformFixture{
		values:    map[string]string{},
		home:      t.TempDir(),
		homeCalls: &homeCalls,
	}
	builder, err := deepseek.NewFactory(deployment)
	if err != nil {
		t.Fatal(err)
	}
	instance, err := builder.Create(
		context.Background(),
		json.RawMessage(`{}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	credentialManager, err := credentials.NewManager(
		credentialStoreFixture{},
		deployment,
	)
	if err != nil {
		t.Fatal(err)
	}
	modelRuntime := llm.NewRuntime(nil)
	runtimeEngine := plugin.NewRuntime(plugin.RuntimeSettings{})
	handles, err := runtimeEngine.Start(
		context.Background(),
		instance,
		modelRuntime,
		credentialManager,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer runtimeEngine.Shutdown(context.Background())

	providers := modelRuntime.ListProviders()
	directory := modelRuntime.ListConfigurableProviders()
	if len(providers) != 1 || providers[0].ID != deepseek.ProviderRoute {
		t.Fatalf("providers = %#v", providers)
	}
	if len(directory) != 1 || directory[0].Provider != deepseek.ProviderRoute {
		t.Fatalf("configurable providers = %#v", directory)
	}
	if homeCalls.Load() != 0 {
		t.Fatalf("identity was resolved during startup: calls=%d", homeCalls.Load())
	}

	if err := runtimeEngine.Unload(context.Background(), handles[0]); err != nil {
		t.Fatal(err)
	}
	if len(modelRuntime.ListProviders()) != 0 || len(modelRuntime.ListConfigurableProviders()) != 0 {
		t.Fatal("DeepSeek contributions survived Plugin unload")
	}
}

func TestPluginRollsBackDirectoryWhenAdapterRegistrationFails(t *testing.T) {
	t.Parallel()
	deployment := platformFixture{
		values: map[string]string{},
		home:   t.TempDir(),
	}
	credentialManager, err := credentials.NewManager(
		credentialStoreFixture{},
		deployment,
	)
	if err != nil {
		t.Fatal(err)
	}
	modelRuntime := llm.NewRuntime(nil)
	runtimeEngine := plugin.NewRuntime(plugin.RuntimeSettings{})
	if _, err := runtimeEngine.Start(
		context.Background(),
		modelRuntime,
		credentialManager,
	); err != nil {
		t.Fatal(err)
	}
	defer runtimeEngine.Shutdown(context.Background())

	conflictHandle, err := modelRuntime.RegisterAdapter(
		context.Background(),
		[]string{deepseek.ProviderRoute},
		&conflictingAdapter{},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer conflictHandle.Release(context.Background())

	builder, err := deepseek.NewFactory(deployment)
	if err != nil {
		t.Fatal(err)
	}
	instance, err := builder.Create(
		context.Background(),
		json.RawMessage(`{}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtimeEngine.Mount(context.Background(), instance); err == nil {
		t.Fatal("Mount succeeded despite an occupied DeepSeek route")
	}
	if len(modelRuntime.ListConfigurableProviders()) != 0 {
		t.Fatal("failed Plugin Apply retained its directory contribution")
	}
}

func TestFactoryRejectsInvalidConfiguration(t *testing.T) {
	t.Parallel()
	builder, err := deepseek.NewFactory(platformFixture{
		values: map[string]string{},
		home:   t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
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
			name:        "nested unknown field",
			rawConfig:   json.RawMessage(`{"models":[{"id":"m","unknown":true}]}`),
			wantMessage: "unknown field",
		},
		{
			name:        "null",
			rawConfig:   json.RawMessage(`null`),
			wantMessage: "JSON object",
		},
		{
			name:        "duplicate field",
			rawConfig:   json.RawMessage(`{"baseURL":"https://one.example","baseURL":"https://two.example"}`),
			wantMessage: "duplicate field",
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
