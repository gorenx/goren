package apiproxy_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/gorenx/goren/apiproxy"
	"github.com/gorenx/goren/connection"
)

func TestHostDescribeDispatch(t *testing.T) {
	t.Parallel()
	methods := apiproxy.NewCatalog()
	source := apiproxy.HostDescriptionFunc(func(context.Context) (apiproxy.HostDescription, error) {
		return apiproxy.HostDescription{
			Version: "0.1.0-rc.5", CWD: "/workspace", Provider: "deepseek", Model: "deepseek-chat",
			AttachedSessions: 2, CanOpenPath: true,
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
	methods := apiproxy.NewCatalog()
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

func TestCatalogSeparatesBusinessAndTechnicalFailure(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		operation apiproxy.UnaryHandler[struct{}, struct{}]
	}{
		{
			name: "returned error",
			operation: func(context.Context, apiproxy.Request[struct{}]) (apiproxy.Outcome[struct{}], error) {
				return apiproxy.Outcome[struct{}]{}, errors.New("dependency failed")
			},
		},
		{
			name: "panic",
			operation: func(context.Context, apiproxy.Request[struct{}]) (apiproxy.Outcome[struct{}], error) {
				panic("provider crashed")
			},
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			methods := apiproxy.NewCatalog()
			if err := apiproxy.RegisterUnary(methods, "test.failure", apiproxy.DecodeObject[struct{}], testCase.operation); err != nil {
				t.Fatal(err)
			}
			if _, err := methods.DispatchUnary(context.Background(), "test.failure", "r-1", json.RawMessage(`{}`)); err == nil {
				t.Fatal("technical failure was not returned")
			}
		})
	}
}

func TestCatalogRejectsDuplicateMethod(t *testing.T) {
	t.Parallel()
	methods := apiproxy.NewCatalog()
	operation := func(context.Context, apiproxy.Request[struct{}]) (apiproxy.Outcome[struct{}], error) {
		return apiproxy.OK(struct{}{}), nil
	}
	if err := apiproxy.RegisterUnary(methods, "test.duplicate", apiproxy.DecodeObject[struct{}], operation); err != nil {
		t.Fatal(err)
	}
	if err := apiproxy.RegisterUnary(methods, "test.duplicate", apiproxy.DecodeObject[struct{}], operation); err == nil {
		t.Fatal("duplicate method was accepted")
	}
}
