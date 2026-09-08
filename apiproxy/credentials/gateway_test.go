package credentialsapi_test

import (
	"context"
	"encoding/json"
	rpc "github.com/gorenx/goren/apiproxy/rpc"
	"strings"
	"testing"

	api "github.com/gorenx/goren/apiproxy/credentials"
	"github.com/gorenx/goren/connection"
	"github.com/gorenx/goren/credentials"
)

type credentialProviderFixture struct {
	value string
}

func (fixture *credentialProviderFixture) Describe(context.Context, string) (credentials.Info, error) {
	return credentials.Info{
		Configured: fixture.value != "",
		Source:     "file",
		Writable:   true,
	}, nil
}

func (fixture *credentialProviderFixture) Set(_ context.Context, _ string, value string) error {
	fixture.value = value
	return nil
}

func (fixture *credentialProviderFixture) Unset(context.Context, string) error {
	fixture.value = ""
	return nil
}

func TestCredentialsGatewayKeepsValuesWriteOnly(t *testing.T) {
	providerFixture := &credentialProviderFixture{}
	methods := rpc.NewCatalog()
	if err := api.Register(methods, api.NewGateway(providerFixture)); err != nil {
		t.Fatal(err)
	}
	const testValue = "test-browser-credential"
	setResult, err := methods.DispatchUnary(context.Background(), api.SetMethod, "set", json.RawMessage(`{"ref":"DEEPSEEK_API_KEY","value":"`+testValue+`"}`))
	if err != nil || !setResult.OK {
		t.Fatalf("set = (%#v, %v)", setResult, err)
	}
	describeResult, err := methods.DispatchUnary(context.Background(), api.DescribeMethod, "describe", json.RawMessage(`{"refs":["DEEPSEEK_API_KEY"]}`))
	if err != nil || !describeResult.OK {
		t.Fatalf("describe = (%#v, %v)", describeResult, err)
	}
	if strings.Contains(string(describeResult.Value), testValue) {
		t.Fatal("credentials.describe exposed the credential value")
	}
	var description api.DescribeValue
	if err := json.Unmarshal(describeResult.Value, &description); err != nil {
		t.Fatal(err)
	}
	view := description.Credentials["DEEPSEEK_API_KEY"]
	if !view.Configured || view.Source != "file" || !view.Writable {
		t.Fatalf("view = %#v", view)
	}
	badResult, err := methods.DispatchUnary(context.Background(), api.SetMethod, "bad", json.RawMessage(`{"ref":"bad-ref","value":"x"}`))
	if err != nil || badResult.OK || badResult.Error == nil || badResult.Error.Code != connection.ErrorBadRequest {
		t.Fatalf("invalid reference = (%#v, %v)", badResult, err)
	}
}
