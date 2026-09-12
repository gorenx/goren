package directorypicker

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/gorenx/goren/apiproxy/rpc"
	"github.com/gorenx/goren/connection"
)

type fixedPicker struct {
	path    *string
	failure error
	calls   int
}

func (source *fixedPicker) Pick(context.Context) (*string, error) {
	source.calls++
	return source.path, source.failure
}

func TestPickWireOutcomes(t *testing.T) {
	chosen := "/workspace"
	cases := []struct {
		name    string
		chooser Picker
		value   string
		code    connection.RPCErrorCode
	}{
		{
			name: "selected",
			chooser: &fixedPicker{
				path: &chosen,
			},
			value: `"/workspace"`,
		},
		{
			name:    "operator cancelled",
			chooser: &fixedPicker{},
			value:   "null",
		},
		{
			name: "absent",
			code: connection.ErrorDirectoryPickerUnavailable,
		},
		{
			name: "unavailable",
			chooser: &fixedPicker{
				failure: ErrUnavailable,
			},
			code: connection.ErrorDirectoryPickerUnavailable,
		},
		{
			name: "request cancelled",
			chooser: &fixedPicker{
				failure: context.Canceled,
			},
			code: connection.ErrorCancelled,
		},
		{
			name: "native failure",
			chooser: &fixedPicker{
				failure: errors.New("desktop unavailable"),
			},
			code: connection.ErrorInternal,
		},
	}
	for _, scenario := range cases {
		t.Run(scenario.name, func(t *testing.T) {
			methods := rpc.NewCatalog()
			if err := Register(methods, scenario.chooser); err != nil {
				t.Fatal(err)
			}
			result, err := methods.DispatchUnary(t.Context(), PickMethod, "pick", json.RawMessage(`{}`))
			if err != nil {
				t.Fatal(err)
			}
			if scenario.code != "" {
				if result.OK ||
					result.Error == nil ||
					result.Error.Code != scenario.code {
					t.Fatalf("result = %+v", result)
				}
			} else if !result.OK || string(result.Value) != scenario.value {
				t.Fatalf("result = %+v", result)
			}
		})
	}
}

func TestPickRejectsPayloadAndCancelledAdmission(t *testing.T) {
	source := &fixedPicker{}
	methods := rpc.NewCatalog()
	if err := Register(methods, source); err != nil {
		t.Fatal(err)
	}
	for _, payload := range []string{`null`, `[]`, `{"path":"/tmp"}`, `{"path":1,"path":2}`, `{} {}`} {
		result, err := methods.DispatchUnary(t.Context(), PickMethod, "pick", json.RawMessage(payload))
		if err != nil ||
			result.OK ||
			result.Error == nil ||
			result.Error.Code != connection.ErrorBadRequest {
			t.Fatalf("payload %s: result=%+v err=%v", payload, result, err)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	result, err := methods.DispatchUnary(ctx, PickMethod, "pick", json.RawMessage(`{}`))
	if err != nil ||
		result.OK ||
		result.Error == nil ||
		result.Error.Code != connection.ErrorCancelled {
		t.Fatalf("cancelled request: result=%+v err=%v", result, err)
	}
	if source.calls != 0 {
		t.Fatalf("invalid requests opened %d windows", source.calls)
	}
}
