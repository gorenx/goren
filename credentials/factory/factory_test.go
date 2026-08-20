package factory_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gorenx/goren/credentials"
	credentialfactory "github.com/gorenx/goren/credentials/factory"
	credentialslocal "github.com/gorenx/goren/credentials/local"
	"github.com/gorenx/goren/plugin"
)

type environmentFixture struct {
	values map[string]string
}

func (environment environmentFixture) Lookup(name string) (string, bool) {
	value, found := environment.values[name]
	return value, found
}

type consumerPlugin struct {
	plugin.Base
	provider credentials.Provider
}

func (*consumerPlugin) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: "credentials-consumer",
		Requires: []plugin.ServiceType{
			plugin.ServiceOf[credentials.Provider](),
		},
	}
}

func (consumer *consumerPlugin) Apply(context.Context) error {
	provider, err := plugin.Require[credentials.Provider](consumer)
	if err != nil {
		return err
	}
	consumer.provider = provider
	return nil
}

func (consumer *consumerPlugin) Dispose(context.Context) error {
	consumer.provider = nil
	return nil
}

func TestFactoryCreatesCredentialsProviderPlugin(t *testing.T) {
	t.Parallel()
	builder, err := credentialfactory.New(environmentFixture{
		values: map[string]string{},
	})
	if err != nil {
		t.Fatal(err)
	}
	rawConfig, err := json.Marshal(credentialfactory.Config{
		Local: credentialslocal.Config{
			Path: filepath.Join(t.TempDir(), ".credentials.json"),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	instance, err := builder.Create(context.Background(), rawConfig)
	if err != nil {
		t.Fatal(err)
	}
	consumer := &consumerPlugin{}
	runtimeEngine := plugin.NewRuntime(plugin.RuntimeSettings{})
	if _, err := runtimeEngine.Start(context.Background(), consumer, instance); err != nil {
		t.Fatal(err)
	}
	defer runtimeEngine.Shutdown(context.Background())
	if consumer.provider == nil {
		t.Fatal("Credentials Provider was not resolved")
	}
}

func TestFactoryRejectsUnknownConfiguration(t *testing.T) {
	t.Parallel()
	builder, err := credentialfactory.New(environmentFixture{
		values: map[string]string{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := builder.Create(
		context.Background(),
		json.RawMessage(`{"local":{"path":"/tmp/credentials.json"},"unknown":true}`),
	); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("Create error = %v", err)
	}
}
