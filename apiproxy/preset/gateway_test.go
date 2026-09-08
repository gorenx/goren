package presetapi_test

import (
	"context"
	"encoding/json"
	"errors"
	rpc "github.com/gorenx/goren/apiproxy/rpc"
	"reflect"
	"testing"

	api "github.com/gorenx/goren/apiproxy/preset"
	"github.com/gorenx/goren/connection"
)

type agentPresetRosterFixture struct {
	entries       []api.Preset
	defaultPreset string
	authoring     bool
	listFailure   error
}

func (fixture *agentPresetRosterFixture) List(context.Context) ([]api.Preset, error) {
	if fixture.listFailure != nil {
		return nil, fixture.listFailure
	}
	return append([]api.Preset{}, fixture.entries...), nil
}

func (fixture *agentPresetRosterFixture) DefaultID() string {
	return fixture.defaultPreset
}

func (fixture *agentPresetRosterFixture) Authorable() bool {
	return fixture.authoring
}

func TestAgentPresetGatewayReportsAbsentRoster(t *testing.T) {
	gateway := api.NewGateway(nil, api.Options{
		CanOpenPath: true,
	})
	got := dispatchAgentPresetList(t, gateway)
	want := api.ListValue{
		Presets: []api.Entry{},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("list = %#v, want %#v", got, want)
	}
}

func TestAgentPresetGatewayProjectsRoster(t *testing.T) {
	name := "Standard"
	description := "Full coding agent"
	broken := "missing plugin"
	roster := &agentPresetRosterFixture{
		entries: []api.Preset{
			{
				ID:          "standard",
				Trust:       api.System,
				Name:        &name,
				Description: &description,
			},
			{
				ID:     "draft",
				Trust:  api.User,
				Broken: &broken,
			},
		},
		defaultPreset: "standard",
		authoring:     true,
	}
	gateway := api.NewGateway(roster, api.Options{
		CanOpenPath: true,
	})
	got := dispatchAgentPresetList(t, gateway)
	want := api.ListValue{
		Presets: []api.Entry{
			{
				ID:          "standard",
				Trust:       api.System,
				IsDefault:   true,
				Name:        &name,
				Description: &description,
			},
			{
				ID:     "draft",
				Trust:  api.User,
				Broken: &broken,
			},
		},
		Authorable:  true,
		HasDocument: true,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("list = %#v, want %#v", got, want)
	}
	name = "Changed"
	if *got.Presets[0].Name != "Standard" {
		t.Fatal("wire entry aliases roster metadata")
	}
}

func TestAgentPresetGatewayRejectsInvalidProviderState(t *testing.T) {
	tests := []struct {
		name    string
		entries []api.Preset
	}{
		{
			name: "empty id",
			entries: []api.Preset{{
				Trust: api.System,
			}},
		},
		{
			name: "invalid trust",
			entries: []api.Preset{{
				ID:    "bad",
				Trust: "remote",
			}},
		},
		{
			name: "duplicate",
			entries: []api.Preset{
				{
					ID:    "same",
					Trust: api.System,
				}, {
					ID:    "same",
					Trust: api.User,
				},
			},
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			gateway := api.NewGateway(
				&agentPresetRosterFixture{
					entries: testCase.entries,
				}, api.Options{},
			)
			methods := rpc.NewCatalog()
			if err := api.Register(methods, gateway); err != nil {
				t.Fatal(err)
			}
			if _, err := methods.DispatchUnary(
				context.Background(), api.ListMethod,
				connection.RPCID("invalid-roster"), json.RawMessage(`{}`),
			); err == nil {
				t.Fatal("invalid roster was accepted")
			}
		})
	}
}

func TestAgentPresetGatewayPropagatesRosterFailure(t *testing.T) {
	gateway := api.NewGateway(
		&agentPresetRosterFixture{
			listFailure: errors.New("roster unavailable"),
		}, api.Options{},
	)
	methods := rpc.NewCatalog()
	if err := api.Register(methods, gateway); err != nil {
		t.Fatal(err)
	}
	if _, err := methods.DispatchUnary(
		context.Background(), api.ListMethod,
		connection.RPCID("roster-failure"), json.RawMessage(`{}`),
	); err == nil || err.Error() != "roster unavailable" {
		t.Fatalf("failure = %v", err)
	}
}

func dispatchAgentPresetList(t *testing.T, gateway *api.Gateway) api.ListValue {
	t.Helper()
	methods := rpc.NewCatalog()
	if err := api.Register(methods, gateway); err != nil {
		t.Fatal(err)
	}
	result, err := methods.DispatchUnary(
		context.Background(), api.ListMethod,
		connection.RPCID("preset-list"), json.RawMessage(`{"ignored":true}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	if !result.OK {
		t.Fatalf("result = %#v", result)
	}
	var value api.ListValue
	if err := json.Unmarshal(result.Value, &value); err != nil {
		t.Fatal(err)
	}
	return value
}
