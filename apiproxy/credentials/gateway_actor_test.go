package credentialsapi_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	api "github.com/gorenx/goren/apiproxy/credentials"
	"github.com/gorenx/goren/apiproxy/rpc"
	"github.com/gorenx/goren/connection"
	"github.com/gorenx/goren/credentials"
	credentialactor "github.com/gorenx/goren/credentials/actor"
	credentiallocal "github.com/gorenx/goren/credentials/local"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/log"
)

type launchEnvironment map[string]string

func (values launchEnvironment) Lookup(name string) (string, bool) {
	value, found := values[name]
	return value, found
}

func TestGatewayUsesCredentialsActor(t *testing.T) {
	ctx := context.Background()
	storage, err := credentiallocal.NewLiveStore(credentiallocal.Config{
		Path: filepath.Join(t.TempDir(), "credentials.json"),
	})
	if err != nil {
		t.Fatal(err)
	}
	business, err := credentials.NewManager(storage, launchEnvironment{
		"INHERITED_KEY": "test-inherited-value",
	})
	if err != nil {
		t.Fatal(err)
	}
	system, err := goakt.NewActorSystem("credentials-api", goakt.WithLogger(log.DiscardLogger))
	if err != nil {
		t.Fatal(err)
	}
	if err := system.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := system.Stop(ctx); err != nil {
			t.Error(err)
		}
	})
	identity, err := goakt.GrainOf[*credentialactor.Grain](ctx, system, credentialactor.Name, goakt.WithLongLivedGrain())
	if err != nil {
		t.Fatal(err)
	}
	payload, err := system.AskGrain(ctx, identity, credentialactor.Initialize{
		Manager: business,
	}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if result := payload.(credentialactor.Completion); result.Err != nil {
		t.Fatal(result.Err)
	}
	endpoint, err := credentialactor.NewClient(system)
	if err != nil {
		t.Fatal(err)
	}
	methods := rpc.NewCatalog()
	if err := api.Register(methods, api.NewGateway(endpoint)); err != nil {
		t.Fatal(err)
	}
	call := func(method string, body string) connection.RPCResult {
		t.Helper()
		result, err := methods.DispatchUnary(ctx, method, "request", json.RawMessage(body))
		if err != nil {
			t.Fatal(err)
		}
		encoded, err := json.Marshal(result)
		if err != nil {
			t.Fatal(err)
		}
		for _, secret := range []string{"test-managed-value", "test-inherited-value"} {
			if strings.Contains(string(encoded), secret) {
				t.Fatal("credential value leaked in RPC response")
			}
		}
		return result
	}
	if result := call(api.SetMethod, `{"ref":"MANAGED_KEY","value":"test-managed-value"}`); !result.OK || string(result.Value) != "{}" {
		t.Fatalf("set = %#v", result)
	}
	describe := func() api.DescribeValue {
		t.Helper()
		result := call(api.DescribeMethod, `{"refs":["MANAGED_KEY","INHERITED_KEY"]}`)
		if !result.OK {
			t.Fatalf("describe = %#v", result)
		}
		var value api.DescribeValue
		if err := json.Unmarshal(result.Value, &value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	value := describe()
	if state := value.Credentials["MANAGED_KEY"]; !state.Configured ||
		!state.Writable ||
		state.Source != "file" {
		t.Fatalf("managed metadata = %#v", state)
	}
	if state := value.Credentials["INHERITED_KEY"]; !state.Configured ||
		state.Writable ||
		state.Source != "env" {
		t.Fatalf("inherited metadata = %#v", state)
	}
	for _, method := range []string{api.SetMethod, api.UnsetMethod} {
		result := call(method, `{"ref":"INHERITED_KEY","value":"test-managed-value"}`)
		if result.OK ||
			result.Error == nil ||
			result.Error.Code != connection.ErrorCredentialRejected {
			t.Fatalf("read-only %s = %#v", method, result)
		}
	}
	for range 2 {
		if result := call(api.UnsetMethod, `{"ref":"MANAGED_KEY"}`); !result.OK || string(result.Value) != "{}" {
			t.Fatalf("unset = %#v", result)
		}
	}
	if describe().Credentials["MANAGED_KEY"].Configured {
		t.Fatal("unset did not remove managed credential")
	}
}
