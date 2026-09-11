package apiproxy_test

import (
	"context"
	"encoding/json"
	rpc "github.com/gorenx/goren/apiproxy/rpc"
	"testing"

	"github.com/gorenx/goren/apiproxy"
	"github.com/gorenx/goren/connection"
)

func TestHostDescribeDispatch(t *testing.T) {
	t.Parallel()
	methods := rpc.NewCatalog()
	source := apiproxy.HostDescriptionFunc(func(context.Context) (apiproxy.HostDescription, error) {
		return apiproxy.HostDescription{
			Version:          "0.1.0-rc.5",
			CWD:              "/workspace",
			Provider:         "deepseek",
			Model:            "deepseek-chat",
			AttachedSessions: 2,
			CanOpenPath:      true,
		}, nil
	})
	if err := apiproxy.RegisterHostDescribe(methods, source); err != nil {
		t.Fatal(err)
	}
	outcome, err := methods.DispatchUnary(context.Background(), apiproxy.HostDescribeMethod, "r-1", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if !outcome.OK || outcome.Error != nil {
		t.Fatalf("outcome = %#v", outcome)
	}
	var snapshot apiproxy.HostDescription
	if err := json.Unmarshal(outcome.Value, &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.Version != "0.1.0-rc.5" || snapshot.AttachedSessions != 2 {
		t.Fatalf("snapshot = %#v", snapshot)
	}
}

func TestCatalogRejectsInvalidPayloadBeforeProvider(t *testing.T) {
	t.Parallel()
	called := false
	methods := rpc.NewCatalog()
	source := apiproxy.HostDescriptionFunc(func(context.Context) (apiproxy.HostDescription, error) {
		called = true
		return apiproxy.HostDescription{}, nil
	})
	if err := apiproxy.RegisterHostDescribe(methods, source); err != nil {
		t.Fatal(err)
	}
	outcome, err := methods.DispatchUnary(context.Background(), apiproxy.HostDescribeMethod, "r-1", json.RawMessage(`null`))
	if err != nil {
		t.Fatal(err)
	}
	if outcome.OK || outcome.Error == nil || outcome.Error.Code != connection.ErrorBadRequest {
		t.Fatalf("outcome = %#v", outcome)
	}
	if called {
		t.Fatal("provider was called for an invalid payload")
	}
}
