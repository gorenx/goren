package factory_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/plugin/configuration"
	"github.com/gorenx/goren/plugin/factory"
)

type configFixture struct {
	configuration.InputBase
	Address string `json:"address"`
}

type configuratorFixture struct {
	configureCalls *atomic.Int64
	createCalls    *atomic.Int64
}

func (configuratorFixture) Name() string {
	return "@deepseek-ai/fixture"
}

func (fixture configuratorFixture) Configure(
	sourceDocument configuration.Document,
) (factory.Factory, error) {
	fixture.configureCalls.Add(1)
	settings, err := configuration.DecodeJSON[configFixture](sourceDocument)
	if err != nil {
		return nil, err
	}
	if !strings.HasPrefix(settings.Address, "127.0.0.1:") {
		return nil, errors.New("address must be loopback")
	}
	return configuredFactoryFixture{
		settings:    settings,
		createCalls: fixture.createCalls,
	}, nil
}

type configuredFactoryFixture struct {
	settings    configFixture
	createCalls *atomic.Int64
}

func (configuredFactoryFixture) Name() string {
	return "@deepseek-ai/fixture"
}

func (fixture configuredFactoryFixture) Create(context.Context) (plugin.Plugin, error) {
	fixture.createCalls.Add(1)
	return pluginFixture{
		address: fixture.settings.Address,
	}, nil
}

type pluginFixture struct {
	address string
}

func (pluginFixture) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: "@deepseek-ai/fixture",
	}
}

func (pluginFixture) Apply(context.Context, *plugin.Context) error {
	return nil
}

func (pluginFixture) Dispose(context.Context) error {
	return nil
}

func TestCatalogOnlyRegistersAndFindsConfigurators(t *testing.T) {
	t.Parallel()
	var configureCalls atomic.Int64
	var createCalls atomic.Int64
	directory := factory.NewCatalog()
	candidate := configuratorFixture{
		configureCalls: &configureCalls,
		createCalls:    &createCalls,
	}
	if err := directory.Register(candidate); err != nil {
		t.Fatal(err)
	}
	if err := directory.Register(candidate); err == nil {
		t.Fatal("duplicate Configurator registration succeeded")
	}
	if got := directory.Names(); !reflect.DeepEqual(got, []string{"@deepseek-ai/fixture"}) {
		t.Fatalf("Configurator names = %#v", got)
	}
	selected, err := directory.Lookup("@deepseek-ai/fixture")
	if err != nil {
		t.Fatal(err)
	}
	if configureCalls.Load() != 0 || createCalls.Load() != 0 {
		t.Fatalf("Catalog executed workflow: configure=%d create=%d", configureCalls.Load(), createCalls.Load())
	}
	if selected.Name() != candidate.Name() {
		t.Fatalf("selected Configurator name = %q", selected.Name())
	}
	if _, err := directory.Lookup("@deepseek-ai/excluded"); err == nil {
		t.Fatal("unregistered Configurator lookup succeeded")
	}
}

func TestConfigurationAndPluginConstructionAreSeparateStages(t *testing.T) {
	t.Parallel()
	var configureCalls atomic.Int64
	var createCalls atomic.Int64
	configurator := configuratorFixture{
		configureCalls: &configureCalls,
		createCalls:    &createCalls,
	}
	sourceDocument, err := configuration.NewDocument(
		[]byte(`{"address":"127.0.0.1:3080"}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	configuredFactory, err := configurator.Configure(sourceDocument)
	if err != nil {
		t.Fatal(err)
	}
	if configureCalls.Load() != 1 || createCalls.Load() != 0 {
		t.Fatalf("after Configure: configure=%d create=%d", configureCalls.Load(), createCalls.Load())
	}
	instance, err := configuredFactory.Create(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if configureCalls.Load() != 1 || createCalls.Load() != 1 {
		t.Fatalf("after Create: configure=%d create=%d", configureCalls.Load(), createCalls.Load())
	}
	if instance.Manifest().Name != configuredFactory.Name() {
		t.Fatalf("Plugin name = %q, Factory name = %q", instance.Manifest().Name, configuredFactory.Name())
	}
}

func TestConfiguratorRejectsOwnerInvalidConfigurationBeforeFactoryCreation(t *testing.T) {
	t.Parallel()
	var configureCalls atomic.Int64
	var createCalls atomic.Int64
	configurator := configuratorFixture{
		configureCalls: &configureCalls,
		createCalls:    &createCalls,
	}
	sourceDocument, err := configuration.NewDocument(
		[]byte(`{"address":"0.0.0.0:3080"}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = configurator.Configure(sourceDocument); err == nil ||
		!strings.Contains(err.Error(), "address must be loopback") {
		t.Fatalf("Configure error = %v", err)
	}
	if createCalls.Load() != 0 {
		t.Fatalf("invalid configuration created %d Plugins", createCalls.Load())
	}
}
