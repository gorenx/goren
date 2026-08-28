package apiproxy

import (
	"context"
	"errors"

	"github.com/gorenx/goren/connection"
	boundcontract "github.com/gorenx/goren/subagent/bound"
)

// BoundGateway maps the transport-neutral Definition capability to Host RPC.
type BoundGateway struct {
	definitions boundcontract.Definitions
}

// NewBoundGateway constructs the Bound Definition API adapter.
func NewBoundGateway(definitions boundcontract.Definitions) *BoundGateway {
	return &BoundGateway{
		definitions: definitions,
	}
}

// List returns the detached committed Definition roster.
func (gateway *BoundGateway) List(
	requestContext context.Context,
	_ Request[BoundListRequest],
) (Outcome[BoundListValue], error) {
	definitions, err := gateway.definitions.List(requestContext)
	if err != nil {
		return Outcome[BoundListValue]{}, err
	}
	return OK(BoundListValue{
		Definitions: definitions,
	}), nil
}

// Create commits and returns the first revision.
func (gateway *BoundGateway) Create(
	requestContext context.Context,
	call Request[boundcontract.Creation],
) (Outcome[BoundDefinitionValue], error) {
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
	return OK(BoundDefinitionValue{
		Definition: definitionValue,
	}), nil
}

// Replace commits and returns one complete next revision.
func (gateway *BoundGateway) Replace(
	requestContext context.Context,
	call Request[boundcontract.Replacement],
) (Outcome[BoundDefinitionValue], error) {
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
	return OK(BoundDefinitionValue{
		Definition: definitionValue,
	}), nil
}

func boundDefinitionFailure[V any](
	name string,
	expectedRevision int64,
	definitionErr error,
) (Outcome[V], error) {
	var typed *boundcontract.Error
	if !errors.As(definitionErr, &typed) {
		return Outcome[V]{}, definitionErr
	}
	switch typed.Code {
	case boundcontract.ErrorDefinitionExists:
		return Fail[V](NewRPCError(
			connection.ErrorBoundDefinitionExists,
			typed.Error(),
			struct {
				Name string `json:"name"`
			}{
				Name: name,
			},
		)), nil
	case boundcontract.ErrorDefinitionNotFound:
		return Fail[V](NewRPCError(
			connection.ErrorBoundDefinitionNotFound,
			typed.Error(),
			struct {
				Name string `json:"name"`
			}{
				Name: name,
			},
		)), nil
	case boundcontract.ErrorDefinitionConflict:
		return Fail[V](NewRPCError(
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
		return Fail[V](NewRPCError(
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
		return Outcome[V]{}, definitionErr
	}
}

var _ BoundAPI = (*BoundGateway)(nil)
