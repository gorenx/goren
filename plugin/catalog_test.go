package plugin_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/gorenx/goren/plugin"
)

type limitFixture struct {
	MaxBodyBytes int64 `json:"maxBodyBytes"`
}

type configFixture struct {
	Address string       `json:"address"`
	Limits  limitFixture `json:"limits"`
}

type factoryFixture struct{}

func (factoryFixture) Name() string {
	return "@deepseek-ai/fixture"
}

func (factoryFixture) DecodeConfig(rawConfig json.RawMessage) (configFixture, error) {
	return plugin.DecodeStrictConfig(rawConfig, func(settings configFixture) error {
		if !strings.HasPrefix(settings.Address, "127.0.0.1:") {
			return errors.New("address must be loopback")
		}
		if settings.Limits.MaxBodyBytes <= 0 {
			return errors.New("maxBodyBytes must be positive")
		}
		return nil
	})
}

func (factoryFixture) New(context.Context, configFixture) (plugin.Plugin, error) {
	return fixturePlugin{
		metadata: plugin.Manifest{Name: "@deepseek-ai/fixture"},
		body:     func(context.Context, *plugin.Scope) error { return nil },
	}, nil
}

func TestDecodeStrictConfigRejectsDynamicAndAmbiguousInput(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		label       string
		input       string
		wantMessage string
	}{
		{label: "unknown field", input: `{"address":"127.0.0.1:3080","limits":{"maxBodyBytes":1},"extra":true}`, wantMessage: "unknown field"},
		{label: "wrong type", input: `{"address":7,"limits":{"maxBodyBytes":1}}`, wantMessage: "cannot unmarshal"},
		{label: "duplicate field", input: `{"address":"127.0.0.1:1","address":"127.0.0.1:2","limits":{"maxBodyBytes":1}}`, wantMessage: "duplicate field"},
		{label: "nested duplicate", input: `{"address":"127.0.0.1:1","limits":{"maxBodyBytes":1,"maxBodyBytes":2}}`, wantMessage: "duplicate field"},
		{label: "invalid combination", input: `{"address":"0.0.0.0:3080","limits":{"maxBodyBytes":1}}`, wantMessage: "address must be loopback"},
		{label: "dynamic expression", input: `!!js/function (() => ({ address: "127.0.0.1:3080" }))`, wantMessage: "invalid config"},
	} {
		testCase := testCase
		t.Run(testCase.label, func(t *testing.T) {
			t.Parallel()
			if _, err := (factoryFixture{}).DecodeConfig(json.RawMessage(testCase.input)); err == nil || !strings.Contains(err.Error(), testCase.wantMessage) {
				t.Fatalf("DecodeConfig error = %v, want containing %q", err, testCase.wantMessage)
			}
		})
	}
}

func TestCatalogKeepsTypeErasureAtFactoryBoundary(t *testing.T) {
	t.Parallel()
	registry := plugin.NewCatalog()
	if err := plugin.RegisterFactory(registry, factoryFixture{}); err != nil {
		t.Fatal(err)
	}
	if err := plugin.RegisterFactory(registry, factoryFixture{}); err == nil {
		t.Fatal("duplicate factory registration succeeded")
	}
	if got := registry.Names(); !reflect.DeepEqual(got, []string{"@deepseek-ai/fixture"}) {
		t.Fatalf("factory names = %#v", got)
	}
	created, err := registry.Create(context.Background(), "@deepseek-ai/fixture", json.RawMessage(
		`{"address":"127.0.0.1:3080","limits":{"maxBodyBytes":1024}}`,
	))
	if err != nil {
		t.Fatal(err)
	}
	if created.Manifest().Name != "@deepseek-ai/fixture" {
		t.Fatalf("created manifest = %#v", created.Manifest())
	}
	if _, err := registry.Create(context.Background(), "@deepseek-ai/excluded", json.RawMessage(`{}`)); err == nil {
		t.Fatal("unregistered factory creation succeeded")
	}
}
