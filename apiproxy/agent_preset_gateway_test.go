package apiproxy_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/gorenx/goren/apiproxy"
	"github.com/gorenx/goren/connection"
)

type agentPresetRosterFixture struct {
	entries       []apiproxy.AgentPreset
	defaultPreset string
	authoring     bool
	listFailure   error
}

func (fixture *agentPresetRosterFixture) List(context.Context) ([]apiproxy.AgentPreset, error) {
	if fixture.listFailure != nil {
		return nil, fixture.listFailure
	}
	return append([]apiproxy.AgentPreset{}, fixture.entries...), nil
}

func (fixture *agentPresetRosterFixture) DefaultID() string {
	return fixture.defaultPreset
}

func (fixture *agentPresetRosterFixture) Authorable() bool {
	return fixture.authoring
}

func TestAgentPresetGatewayReportsAbsentRoster(t *testing.T) {
	gateway := apiproxy.NewAgentPresetGateway(nil, apiproxy.AgentPresetGatewayOptions{CanOpenPath: true})
	got := dispatchAgentPresetList(t, gateway)
	want := apiproxy.AgentPresetListValue{Presets: []apiproxy.AgentPresetEntry{}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("list = %#v, want %#v", got, want)
	}
}

func TestAgentPresetGatewayProjectsRoster(t *testing.T) {
	name := "Standard"
	description := "Full coding agent"
	broken := "missing plugin"
	roster := &agentPresetRosterFixture{
		entries: []apiproxy.AgentPreset{
			{ID: "standard", Trust: apiproxy.AgentPresetSystem, Name: &name, Description: &description},
			{ID: "draft", Trust: apiproxy.AgentPresetUser, Broken: &broken},
		},
		defaultPreset: "standard",
		authoring:     true,
	}
	gateway := apiproxy.NewAgentPresetGateway(roster, apiproxy.AgentPresetGatewayOptions{CanOpenPath: true})
	got := dispatchAgentPresetList(t, gateway)
	want := apiproxy.AgentPresetListValue{
		Presets: []apiproxy.AgentPresetEntry{
			{
				ID: "standard", Trust: apiproxy.AgentPresetSystem, IsDefault: true,
				Name: &name, Description: &description,
			},
			{ID: "draft", Trust: apiproxy.AgentPresetUser, Broken: &broken},
		},
		Authorable: true, HasDocument: true,
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
		entries []apiproxy.AgentPreset
	}{
		{name: "empty id", entries: []apiproxy.AgentPreset{{Trust: apiproxy.AgentPresetSystem}}},
		{name: "invalid trust", entries: []apiproxy.AgentPreset{{ID: "bad", Trust: "remote"}}},
		{name: "duplicate", entries: []apiproxy.AgentPreset{
			{ID: "same", Trust: apiproxy.AgentPresetSystem}, {ID: "same", Trust: apiproxy.AgentPresetUser},
		}},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			gateway := apiproxy.NewAgentPresetGateway(
				&agentPresetRosterFixture{entries: testCase.entries}, apiproxy.AgentPresetGatewayOptions{},
			)
			methods := apiproxy.NewCatalog()
			if err := apiproxy.RegisterAgentPresetListAPI(methods, gateway); err != nil {
				t.Fatal(err)
			}
			if _, err := methods.DispatchUnary(
				context.Background(), apiproxy.AgentPresetListMethod,
				connection.RPCID("invalid-roster"), json.RawMessage(`{}`),
			); err == nil {
				t.Fatal("invalid roster was accepted")
			}
		})
	}
}

func TestAgentPresetGatewayPropagatesRosterFailure(t *testing.T) {
	gateway := apiproxy.NewAgentPresetGateway(
		&agentPresetRosterFixture{listFailure: errors.New("roster unavailable")}, apiproxy.AgentPresetGatewayOptions{},
	)
	methods := apiproxy.NewCatalog()
	if err := apiproxy.RegisterAgentPresetListAPI(methods, gateway); err != nil {
		t.Fatal(err)
	}
	if _, err := methods.DispatchUnary(
		context.Background(), apiproxy.AgentPresetListMethod,
		connection.RPCID("roster-failure"), json.RawMessage(`{}`),
	); err == nil || err.Error() != "roster unavailable" {
		t.Fatalf("failure = %v", err)
	}
}

func dispatchAgentPresetList(t *testing.T, gateway *apiproxy.AgentPresetGateway) apiproxy.AgentPresetListValue {
	t.Helper()
	methods := apiproxy.NewCatalog()
	if err := apiproxy.RegisterAgentPresetListAPI(methods, gateway); err != nil {
		t.Fatal(err)
	}
	result, err := methods.DispatchUnary(
		context.Background(), apiproxy.AgentPresetListMethod,
		connection.RPCID("preset-list"), json.RawMessage(`{"ignored":true}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	if !result.OK {
		t.Fatalf("result = %#v", result)
	}
	var value apiproxy.AgentPresetListValue
	if err := json.Unmarshal(result.Value, &value); err != nil {
		t.Fatal(err)
	}
	return value
}
