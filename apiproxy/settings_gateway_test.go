package apiproxy_test

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/gorenx/goren/apiproxy"
	"github.com/gorenx/goren/connection"
)

type settingsDescriberFixture struct {
	description apiproxy.SettingsDescribeValue
	failure     error
}

func (fixture *settingsDescriberFixture) DescribeSettings(context.Context) (apiproxy.SettingsDescribeValue, error) {
	return fixture.description, fixture.failure
}

func TestSettingsGatewayReportsAbsentProvider(t *testing.T) {
	gateway := apiproxy.NewSettingsGateway(nil)
	methods := apiproxy.NewCatalog()
	if err := apiproxy.RegisterSettingsDescribeAPI(methods, gateway); err != nil {
		t.Fatal(err)
	}
	result, err := methods.DispatchUnary(
		context.Background(), apiproxy.SettingsDescribeMethod,
		connection.RPCID("settings-absent"), json.RawMessage(`{}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.OK || result.Error == nil || result.Error.Code != connection.ErrorInternal {
		t.Fatalf("result = %#v", result)
	}
	want := "settings service is absent: this deployment does not mount a settings provider (e.g. @deepseek-ai/dsh-settings-file) in its composition"
	if result.Error.Message != want || string(result.Error.Details) != `{}` {
		t.Fatalf("error = %#v", result.Error)
	}
}

func TestSettingsGatewayProjectsDetachedCatalog(t *testing.T) {
	base := json.RawMessage(`{"apiKeyEnv":"DEEPSEEK_API_KEY"}`)
	describer := &settingsDescriberFixture{description: apiproxy.SettingsDescribeValue{
		Writable: false, HasDocument: true,
		Namespaces: []apiproxy.SettingsNamespaceView{{
			NS: "llm-deepseek", Schema: json.RawMessage(`{"uid":1,"refs":{}}`),
			Value: json.RawMessage(`{"apiKeyEnv":"DEEPSEEK_API_KEY"}`), Base: &base,
			Applies: apiproxy.SettingsApplyRestart,
			Secrets: []apiproxy.SettingsSecretView{{Path: []string{"apiKey"}, Set: false}}, Revision: 2,
		}},
	}}
	gateway := apiproxy.NewSettingsGateway(describer)
	methods := apiproxy.NewCatalog()
	if err := apiproxy.RegisterSettingsDescribeAPI(methods, gateway); err != nil {
		t.Fatal(err)
	}
	result, err := methods.DispatchUnary(
		context.Background(), apiproxy.SettingsDescribeMethod,
		connection.RPCID("settings-present"), json.RawMessage(`{"ignored":true}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	var got apiproxy.SettingsDescribeValue
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
		namespace apiproxy.SettingsNamespaceView
	}{
		{name: "empty namespace", namespace: apiproxy.SettingsNamespaceView{
			Schema: json.RawMessage(`{}`), Value: json.RawMessage(`{}`), Applies: apiproxy.SettingsApplyLive,
		}},
		{name: "invalid applies", namespace: apiproxy.SettingsNamespaceView{
			NS: "bad", Schema: json.RawMessage(`{}`), Value: json.RawMessage(`{}`), Applies: "later",
		}},
		{name: "invalid json", namespace: apiproxy.SettingsNamespaceView{
			NS: "bad", Schema: json.RawMessage(`{`), Value: json.RawMessage(`{}`), Applies: apiproxy.SettingsApplyLive,
		}},
		{name: "negative revision", namespace: apiproxy.SettingsNamespaceView{
			NS: "bad", Schema: json.RawMessage(`{}`), Value: json.RawMessage(`{}`),
			Applies: apiproxy.SettingsApplyLive, Revision: -1,
		}},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			gateway := apiproxy.NewSettingsGateway(&settingsDescriberFixture{
				description: apiproxy.SettingsDescribeValue{
					Namespaces: []apiproxy.SettingsNamespaceView{testCase.namespace},
				},
			})
			methods := apiproxy.NewCatalog()
			if err := apiproxy.RegisterSettingsDescribeAPI(methods, gateway); err != nil {
				t.Fatal(err)
			}
			if _, err := methods.DispatchUnary(
				context.Background(), apiproxy.SettingsDescribeMethod,
				connection.RPCID("settings-invalid"), json.RawMessage(`{}`),
			); err == nil {
				t.Fatal("invalid Settings Provider catalog was accepted")
			}
		})
	}
}
