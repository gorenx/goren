// Package directorypicker exposes the Host's native folder selection capability.
package directorypicker

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/gorenx/goren/apiproxy/rpc"
	"github.com/gorenx/goren/connection"
)

// PickMethod opens one native folder selection window.
const PickMethod = "directoryPicker.pick"

// ErrUnavailable means no native chooser can serve this request.
var ErrUnavailable = errors.New("native directory picker is unavailable")

// Picker is the consumer-owned native directory selection port.
// A nil path with no error means the operator dismissed the window.
type Picker interface {
	Pick(context.Context) (*string, error)
}

// Gateway maps native selection outcomes without owning desktop resources.
type Gateway struct {
	chooser Picker
}

// NewGateway accepts an optional native chooser; nil reports unavailability.
func NewGateway(source Picker) *Gateway {
	return &Gateway{
		chooser: source,
	}
}

// Pick returns a selected directory or nil for user cancellation.
func (owner *Gateway) Pick(ctx context.Context, _ rpc.Request[struct{}]) (rpc.Outcome[*string], error) {
	if err := ctx.Err(); err != nil {
		return cancelled(), nil
	}
	if owner.chooser == nil {
		return unavailable(), nil
	}
	path, err := owner.chooser.Pick(ctx)
	if ctx.Err() != nil ||
		errors.Is(err, context.Canceled) ||
		errors.Is(err, context.DeadlineExceeded) {
		return cancelled(), nil
	}
	if errors.Is(err, ErrUnavailable) {
		return unavailable(), nil
	}
	if err != nil {
		return rpc.Fail[*string](rpc.NewRPCError(connection.ErrorInternal, "native folder selection failed: "+err.Error(), struct{}{})), nil
	}
	return rpc.OK(path), nil
}

func cancelled() rpc.Outcome[*string] {
	return rpc.Fail[*string](rpc.NewRPCError(connection.ErrorCancelled, "folder selection was cancelled", struct{}{}))
}

func unavailable() rpc.Outcome[*string] {
	return rpc.Fail[*string](rpc.NewRPCError(
		connection.ErrorDirectoryPickerUnavailable,
		"native folder selection is unavailable or a selection window is already open",
		struct{}{},
	))
}

// Register installs the native chooser on the existing strict unary RPC carrier.
func Register(methods *rpc.Catalog, source Picker) error {
	owner := NewGateway(source)
	return rpc.RegisterUnary(methods, PickMethod, decodePick, owner.Pick)
}

func decodePick(rawPayload json.RawMessage) (struct{}, []connection.ValidationIssue) {
	fields, issues := rpc.ObjectFields(rawPayload)
	if len(issues) == 0 && len(fields) != 0 {
		issues = []connection.ValidationIssue{{
			Code:    "unrecognized_keys",
			Path:    []string{},
			Message: "directoryPicker.pick accepts an empty object",
		}}
	}
	return struct{}{}, issues
}
