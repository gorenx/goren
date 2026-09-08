package settingsapi_test

import (
	"context"
	"encoding/json"
	rpc "github.com/gorenx/goren/apiproxy/rpc"
	"reflect"
	"testing"

	api "github.com/gorenx/goren/apiproxy/settings"
	"github.com/gorenx/goren/connection"
)

type settingsDescriberFixture struct {
	description api.DescribeValue
	failure     error
}

func (fixture *settingsDescriberFixture) DescribeSettings(context.Context) (api.DescribeValue, error) {
	return fixture.description, fixture.failure
}

func TestSettingsGatewayReportsAbsentProvider(t *testing.T) {
	gateway := api.NewGateway(nil)
	methods := rpc.NewCatalog()
	if err := api.Register(methods, gateway); err != nil {
		t.Fatal(err)
	}
	result, err := methods.DispatchUnary(
		context.Background(), api.DescribeMethod,
		connection.RPCID("settings-absent"), json.RawMessage(`{}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.OK ||
		result.Error == nil ||
		result.Error.Code != connection.ErrorInternal {
		t.Fatalf("result = %#v", result)
	}
	want := "settings service is absent: this deployment does not mount a settings provider (e.g. @deepseek-ai/dsh-settings-file) in its composition"
	if result.Error.Message != want || string(result.Error.Details) != `{}` {
		t.Fatalf("error = %#v", result.Error)
	}
}

func TestSettingsGatewayProjectsDetachedCatalog(t *testing.T) {
	base := json.RawMessage(`{"apiKeyEnv":"DEEPSEEK_API_KEY"}`)
	describer := &settingsDescriberFixture{
		description: api.DescribeValue{
			Writable:    false,
			HasDocument: true,
			Namespaces: []api.NamespaceView{{
				NS:      "llm-deepseek",
				Schema:  json.RawMessage(`{"uid":1,"refs":{}}`),
				Value:   json.RawMessage(`{"apiKeyEnv":"DEEPSEEK_API_KEY"}`),
				Base:    &base,
				Applies: api.ApplyRestart,
				Secrets: []api.SecretView{{
					Path: []string{"apiKey"},
					Set:  false,
				}},
				Revision: 2,
			}},
		},
	}
	gateway := api.NewGateway(describer)
	methods := rpc.NewCatalog()
	if err := api.Register(methods, gateway); err != nil {
		t.Fatal(err)
	}
	result, err := methods.DispatchUnary(
		context.Background(), api.DescribeMethod,
		connection.RPCID("settings-present"), json.RawMessage(`{"ignored":true}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	var got api.DescribeValue
	if err := json.Unmarshal(result.Value, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, describer.description) {
		t.Fatalf("description = %#v, want %#v", got, describer.description)
	}
	describer.description.Namespaces[0].Secrets[0].Path[0] = "changed"
	if got.Namespaces[0].Secrets[0].Path[0] != "apiKey" {
		t.Fatal("wire Settings description aliases Provider state")
	}
}

func TestSettingsGatewayRejectsInvalidProviderCatalog(t *testing.T) {
	tests := []struct {
		name      string
		namespace api.NamespaceView
	}{
		{
			name: "empty namespace",
			namespace: api.NamespaceView{
				Schema:  json.RawMessage(`{}`),
				Value:   json.RawMessage(`{}`),
				Applies: api.ApplyLive,
			},
		},
		{
			name: "invalid applies",
			namespace: api.NamespaceView{
				NS:      "bad",
				Schema:  json.RawMessage(`{}`),
				Value:   json.RawMessage(`{}`),
				Applies: "later",
			},
		},
		{
			name: "invalid json",
			namespace: api.NamespaceView{
				NS:      "bad",
				Schema:  json.RawMessage(`{`),
				Value:   json.RawMessage(`{}`),
				Applies: api.ApplyLive,
			},
		},
		{
			name: "negative revision",
			namespace: api.NamespaceView{
				NS:       "bad",
				Schema:   json.RawMessage(`{}`),
				Value:    json.RawMessage(`{}`),
				Applies:  api.ApplyLive,
				Revision: -1,
			},
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			gateway := api.NewGateway(&settingsDescriberFixture{
				description: api.DescribeValue{
					Namespaces: []api.NamespaceView{testCase.namespace},
				},
			})
			methods := rpc.NewCatalog()
			if err := api.Register(methods, gateway); err != nil {
				t.Fatal(err)
			}
			if _, err := methods.DispatchUnary(
				context.Background(), api.DescribeMethod,
				connection.RPCID("settings-invalid"), json.RawMessage(`{}`),
			); err == nil {
				t.Fatal("invalid Settings Provider catalog was accepted")
			}
		})
	}
}
