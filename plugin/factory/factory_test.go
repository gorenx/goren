package factory_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/plugin/factory"
)

type configFixture struct {
	Address string `json:"address"`
}

type factoryFixture struct {
	pluginConstructions *atomic.Int64
}

func (factoryFixture) Name() string {
	return "@deepseek-ai/fixture"
}

func (fixture factoryFixture) Create(
	createContext context.Context,
	rawConfig json.RawMessage,
) (plugin.Plugin, error) {
	if err := createContext.Err(); err != nil {
		return nil, err
	}
	settings, err := decodeConfig(rawConfig)
	if err != nil {
		return nil, err
	}
	if !strings.HasPrefix(settings.Address, "127.0.0.1:") {
		return nil, errors.New("address must be loopback")
	}
	fixture.pluginConstructions.Add(1)
	return &pluginFixture{
		address: settings.Address,
	}, nil
}

func decodeConfig(rawConfig json.RawMessage) (configFixture, error) {
	var settings configFixture
	decoder := json.NewDecoder(bytes.NewReader(rawConfig))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&settings); err != nil {
		return configFixture{}, err
	}
	var trailingValue json.RawMessage
	if err := decoder.Decode(&trailingValue); !errors.Is(err, io.EOF) {
		if err == nil {
			return configFixture{}, errors.New("configuration contains multiple JSON values")
		}
		return configFixture{}, err
	}
	return settings, nil
}

type pluginFixture struct {
	plugin.Base
	address string
}

func (*pluginFixture) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: "@deepseek-ai/fixture",
	}
}

func (*pluginFixture) Apply(context.Context) error {
	return nil
}

func (*pluginFixture) Dispose(context.Context) error {
	return nil
}

func TestCatalogOnlyRegistersAndFindsFactories(t *testing.T) {
	t.Parallel()
	var pluginConstructions atomic.Int64
	directory := factory.NewCatalog()
	candidate := factoryFixture{
		pluginConstructions: &pluginConstructions,
	}
	if err := directory.Register(candidate); err != nil {
		t.Fatal(err)
	}
	if err := directory.Register(candidate); err == nil {
		t.Fatal("duplicate Factory registration succeeded")
	}
	if got := directory.Names(); !reflect.DeepEqual(got, []string{"@deepseek-ai/fixture"}) {
		t.Fatalf("Factory names = %#v", got)
	}
	selected, err := directory.Lookup("@deepseek-ai/fixture")
	if err != nil {
		t.Fatal(err)
	}
	if pluginConstructions.Load() != 0 {
		t.Fatalf("Catalog constructed %d Plugins", pluginConstructions.Load())
	}
	if selected.Name() != candidate.Name() {
		t.Fatalf("selected Factory name = %q", selected.Name())
	}
	if _, err := directory.Lookup("@deepseek-ai/excluded"); err == nil {
		t.Fatal("unregistered Factory lookup succeeded")
	}
}

func TestFactoryOwnsTypedConfigurationAndPluginConstruction(t *testing.T) {
	t.Parallel()
	var pluginConstructions atomic.Int64
	candidate := factoryFixture{
		pluginConstructions: &pluginConstructions,
	}
	instance, err := candidate.Create(
		context.Background(),
		json.RawMessage(`{"address":"127.0.0.1:3080"}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	if pluginConstructions.Load() != 1 {
		t.Fatalf("Plugin constructions = %d", pluginConstructions.Load())
	}
	if instance.Manifest().Name != candidate.Name() {
		t.Fatalf("Plugin name = %q, Factory name = %q", instance.Manifest().Name, candidate.Name())
	}
}

func TestFactoryRejectsInvalidConfigurationBeforePluginConstruction(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		label       string
		rawConfig   json.RawMessage
		wantMessage string
	}{
		{
			label:       "unknown field",
			rawConfig:   json.RawMessage(`{"address":"127.0.0.1:3080","extra":true}`),
			wantMessage: "unknown field",
		},
		{
			label:       "owner validation",
			rawConfig:   json.RawMessage(`{"address":"0.0.0.0:3080"}`),
			wantMessage: "address must be loopback",
		},
	} {
		selectedCase := testCase
		t.Run(selectedCase.label, func(t *testing.T) {
			t.Parallel()
			var pluginConstructions atomic.Int64
			candidate := factoryFixture{
				pluginConstructions: &pluginConstructions,
			}
			if _, err := candidate.Create(context.Background(), selectedCase.rawConfig); err == nil ||
				!strings.Contains(err.Error(), selectedCase.wantMessage) {
				t.Fatalf("Create error = %v, want containing %q", err, selectedCase.wantMessage)
			}
			if pluginConstructions.Load() != 0 {
				t.Fatalf("invalid configuration constructed %d Plugins", pluginConstructions.Load())
			}
		})
	}
}
