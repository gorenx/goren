package rpc_test

import (
	"context"
	"encoding/json"
	"errors"
	rpc "github.com/gorenx/goren/apiproxy/rpc"
	"github.com/gorenx/goren/connection"
	"testing"
)

func TestCatalogSeparatesBusinessAndTechnicalFailure(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		operation rpc.UnaryHandler[struct{}, struct{}]
	}{
		{
			name: "returned error",
			operation: func(context.Context, rpc.Request[struct{}]) (rpc.Outcome[struct{}], error) {
				return rpc.Outcome[struct{}]{}, errors.New("dependency failed")
			},
		},
		{
			name: "panic",
			operation: func(context.Context, rpc.Request[struct{}]) (rpc.Outcome[struct{}], error) {
				panic("provider crashed")
			},
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			methods := rpc.NewCatalog()
			if err := rpc.RegisterUnary(methods, "test.failure", rpc.DecodeObject[struct{}], testCase.operation); err != nil {
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
	methods := rpc.NewCatalog()
	operation := func(context.Context, rpc.Request[struct{}]) (rpc.Outcome[struct{}], error) {
		return rpc.OK(struct{}{}), nil
	}
	if err := rpc.RegisterUnary(methods, "test.duplicate", rpc.DecodeObject[struct{}], operation); err != nil {
		t.Fatal(err)
	}
	if err := rpc.RegisterUnary(methods, "test.duplicate", rpc.DecodeObject[struct{}], operation); err == nil {
		t.Fatal("duplicate method was accepted")
	}
}

func TestRemoteOutcomesRemainInsideRPCEnvelope(t *testing.T) {
	for _, scenario := range []struct {
		name      string
		operation rpc.UnaryHandler[struct{}, struct{}]
		absent    bool
	}{
		{
			name: "unrecognized command",
			operation: func(context.Context, rpc.Request[struct{}]) (rpc.Outcome[struct{}], error) {
				return rpc.Absent[struct{}](), nil
			},
			absent: true,
		},
		{
			name: "technical failure",
			operation: func(context.Context, rpc.Request[struct{}]) (rpc.Outcome[struct{}], error) {
				return rpc.Outcome[struct{}]{}, errors.New("dependency failed")
			},
		},
		{
			name: "panic",
			operation: func(context.Context, rpc.Request[struct{}]) (rpc.Outcome[struct{}], error) {
				panic("handler failed")
			},
		},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			methods := rpc.NewCatalog()
			if err := rpc.RegisterRemoteUnary(methods, "commands/execute", func(json.RawMessage) (struct{}, *connection.RPCError) {
				return struct{}{}, nil
			}, scenario.operation); err != nil {
				t.Fatal(err)
			}
			result, err := methods.DispatchUnary(context.Background(), "commands/execute", "request", json.RawMessage(`{"args":{}}`))
			if err != nil {
				t.Fatalf("Remote failure escaped RPC envelope: %v", err)
			}
			if scenario.absent {
				if !result.OK || result.Error != nil || len(result.Value) != 0 {
					t.Fatalf("absent result = %#v", result)
				}
				encoded, err := json.Marshal(result)
				if err != nil {
					t.Fatal(err)
				}
				var fields map[string]json.RawMessage
				if err := json.Unmarshal(encoded, &fields); err != nil {
					t.Fatal(err)
				}
				if _, found := fields["value"]; found {
					t.Fatalf("absent value encoded: %s", encoded)
				}
			} else if result.OK || result.Error == nil || result.Error.Code != connection.ErrorInternal {
				t.Fatalf("Remote failure = %#v", result)
			}
		})
	}
}
