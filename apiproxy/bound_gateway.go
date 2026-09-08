package apiproxy

import (
	"context"
	"errors"
	rpc "github.com/gorenx/goren/apiproxy/rpc"

	"github.com/gorenx/goren/connection"
	boundcontract "github.com/gorenx/goren/subagent/bound"
)

// BoundToolDirectory provides the root Tool choices consumed by Bound config.
// It does not expose Tool execution or mutation to API Proxy.
type BoundToolDirectory interface {
	ListBoundTools(context.Context) ([]BoundToolOption, error)
}

// BoundToolDirectoryFunc adapts a composition-root projection function.
type BoundToolDirectoryFunc func(context.Context) ([]BoundToolOption, error)

// ListBoundTools invokes the adapted projection function.
func (operation BoundToolDirectoryFunc) ListBoundTools(
	requestContext context.Context,
) ([]BoundToolOption, error) {
	return operation(requestContext)
}

// BoundExtensionDirectory provides named Extension choices consumed by Bound
// config. It does not expose registration or installation behavior.
type BoundExtensionDirectory interface {
	ListBoundExtensions(context.Context) ([]BoundExtensionOption, error)
}

// BoundExtensionDirectoryFunc adapts a composition-root projection function.
type BoundExtensionDirectoryFunc func(context.Context) ([]BoundExtensionOption, error)

// ListBoundExtensions invokes the adapted projection function.
func (operation BoundExtensionDirectoryFunc) ListBoundExtensions(
	requestContext context.Context,
) ([]BoundExtensionOption, error) {
	return operation(requestContext)
}

// BoundGatewayDependencies contains the Definition capability and the two
// independent catalogs projected by the Bound configuration API.
type BoundGatewayDependencies struct {
	Definitions boundcontract.Definitions
	Tools       BoundToolDirectory
	Extensions  BoundExtensionDirectory
}

// BoundGateway maps the transport-neutral Definition capability to Host RPC.
type BoundGateway struct {
	definitions        boundcontract.Definitions
	toolDirectory      BoundToolDirectory
	extensionDirectory BoundExtensionDirectory
}

// NewBoundGateway constructs the Bound Definition API adapter.
func NewBoundGateway(
	dependencies BoundGatewayDependencies,
) *BoundGateway {
	return &BoundGateway{
		definitions:        dependencies.Definitions,
		toolDirectory:      dependencies.Tools,
		extensionDirectory: dependencies.Extensions,
	}
}

// List returns the detached committed Definition roster.
func (gateway *BoundGateway) List(
	requestContext context.Context,
	_ rpc.Request[BoundListRequest],
) (rpc.Outcome[BoundListValue], error) {
	definitions, err := gateway.definitions.List(requestContext)
	if err != nil {
		return rpc.Outcome[BoundListValue]{}, err
	}
	return rpc.OK(BoundListValue{
		Definitions: definitions,
	}), nil
}

// Extensions returns named selectable Extensions without exposing common
// Extensions or Registry mutation.
func (gateway *BoundGateway) Extensions(
	requestContext context.Context,
	_ rpc.Request[BoundExtensionsRequest],
) (rpc.Outcome[BoundExtensionsValue], error) {
	extensionOptions, err := gateway.extensionDirectory.ListBoundExtensions(
		requestContext,
	)
	if err != nil {
		return rpc.Outcome[BoundExtensionsValue]{}, err
	}
	return rpc.OK(BoundExtensionsValue{
		Extensions: extensionOptions,
	}), nil
}

// Tools returns the root Tool choices without exposing their schemas or runtime.
func (gateway *BoundGateway) Tools(
	requestContext context.Context,
	_ rpc.Request[BoundToolsRequest],
) (rpc.Outcome[BoundToolsValue], error) {
	toolOptions, err := gateway.toolDirectory.ListBoundTools(requestContext)
	if err != nil {
		return rpc.Outcome[BoundToolsValue]{}, err
	}
	return rpc.OK(BoundToolsValue{
		Tools: toolOptions,
	}), nil
}

// Create commits and returns the first revision.
func (gateway *BoundGateway) Create(
	requestContext context.Context,
	call rpc.Request[boundcontract.Creation],
) (rpc.Outcome[BoundDefinitionValue], error) {
	definitionValue, err := gateway.definitions.Create(
		requestContext,
		call.Payload,
	)
	if err != nil {
		return boundDefinitionFailure[BoundDefinitionValue](
			call.Payload.Definition.Name,
			0,
			err,
		)
	}
	return rpc.OK(BoundDefinitionValue{
		Definition: definitionValue,
	}), nil
}

// Replace commits and returns one complete next revision.
func (gateway *BoundGateway) Replace(
	requestContext context.Context,
	call rpc.Request[boundcontract.Replacement],
) (rpc.Outcome[BoundDefinitionValue], error) {
	definitionValue, err := gateway.definitions.Replace(
		requestContext,
		call.Payload,
	)
	if err != nil {
		return boundDefinitionFailure[BoundDefinitionValue](
			call.Payload.Definition.Name,
			call.Payload.ExpectedRevision,
			err,
		)
	}
	return rpc.OK(BoundDefinitionValue{
		Definition: definitionValue,
	}), nil
}

func boundDefinitionFailure[V any](
	name string,
	expectedRevision int64,
	definitionErr error,
) (rpc.Outcome[V], error) {
	var typed *boundcontract.Error
	if !errors.As(definitionErr, &typed) {
		return rpc.Outcome[V]{}, definitionErr
	}
	switch typed.Code {
	case boundcontract.ErrorDefinitionExists:
		return rpc.Fail[V](rpc.NewRPCError(
			connection.ErrorBoundDefinitionExists,
			typed.Error(),
			struct {
				Name string `json:"name"`
			}{
				Name: name,
			},
		)), nil
	case boundcontract.ErrorDefinitionNotFound:
		return rpc.Fail[V](rpc.NewRPCError(
			connection.ErrorBoundDefinitionNotFound,
			typed.Error(),
			struct {
				Name string `json:"name"`
			}{
				Name: name,
			},
		)), nil
	case boundcontract.ErrorDefinitionConflict:
		return rpc.Fail[V](rpc.NewRPCError(
			connection.ErrorBoundDefinitionConflict,
			typed.Error(),
			struct {
				Name             string `json:"name"`
				ExpectedRevision int64  `json:"expectedRevision"`
			}{
				Name:             name,
				ExpectedRevision: expectedRevision,
			},
		)), nil
	case boundcontract.ErrorDefinitionRejected:
		return rpc.Fail[V](rpc.NewRPCError(
			connection.ErrorBoundDefinitionRejected,
			typed.Error(),
			struct {
				Name   string `json:"name"`
				Reason string `json:"reason"`
			}{
				Name:   name,
				Reason: typed.Error(),
			},
		)), nil
	default:
		return rpc.Outcome[V]{}, definitionErr
	}
}

var _ BoundAPI = (*BoundGateway)(nil)
