package apiproxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/gorenx/goren/connection"
)

// Request is the transport-neutral narrow request passed to an API handler.
type Request[P any] struct {
	RPCID   connection.RPCID
	Payload P
}

// Outcome represents one typed business success or failure. Technical handler
// failures are returned separately as Go errors.
type Outcome[V any] struct {
	value    V
	rpcError *connection.RPCError
}

// OK constructs a typed business success.
func OK[V any](value V) Outcome[V] {
	return Outcome[V]{value: value}
}

// Fail constructs a typed business failure.
func Fail[V any](rpcError connection.RPCError) Outcome[V] {
	return Outcome[V]{rpcError: &rpcError}
}

// NewRPCError encodes typed details into the canonical wire error.
func NewRPCError[D any](code connection.RPCErrorCode, message string, details D) connection.RPCError {
	encoded, err := json.Marshal(details)
	if err != nil {
		panic(err)
	}
	return connection.RPCError{Code: code, Message: message, Details: encoded}
}

// PayloadDecoder owns the second-level parse for one method payload.
type PayloadDecoder[P any] func(json.RawMessage) (P, []connection.ValidationIssue)

// UnaryHandler handles one typed API request. It returns business errors in
// Outcome and reserves Go error for handler or dependency failure.
type UnaryHandler[P, V any] func(context.Context, Request[P]) (Outcome[V], error)

type unaryRoute func(context.Context, connection.RPCID, json.RawMessage) (connection.RPCResult, error)

// Catalog owns the statically assembled unary routes and the runtime pending
// response correlation table consumed by Connection. Route registration is
// safe during composition; duplicate ownership is rejected.
type Catalog struct {
	routeMutex   sync.RWMutex
	routes       map[string]unaryRoute
	pendingMutex sync.Mutex
	pending      map[connection.RPCID]*pendingEntry
}

// NewCatalog creates an empty API method catalog.
func NewCatalog() *Catalog {
	return &Catalog{
		routes:  make(map[string]unaryRoute),
		pending: make(map[connection.RPCID]*pendingEntry),
	}
}

// RegisterUnary adds one canonical method with its typed decoder and handler.
func RegisterUnary[P, V any](methods *Catalog, method string, decodePayload PayloadDecoder[P], operation UnaryHandler[P, V]) error {
	if methods == nil {
		return errors.New("apiproxy: catalog is nil")
	}
	if method == "" {
		return errors.New("apiproxy: method is empty")
	}
	if decodePayload == nil {
		return fmt.Errorf("apiproxy: decoder for %q is nil", method)
	}
	if operation == nil {
		return fmt.Errorf("apiproxy: handler for %q is nil", method)
	}

	route := func(requestContext context.Context, rpcID connection.RPCID, rawPayload json.RawMessage) (connection.RPCResult, error) {
		payload, issues := decodePayload(rawPayload)
		if len(issues) != 0 {
			return connection.Failure(connection.BadRequest("invalid payload for "+method, issues)), nil
		}
		businessOutcome, err := invoke(operation, requestContext, Request[P]{RPCID: rpcID, Payload: payload})
		if err != nil {
			return connection.RPCResult{}, err
		}
		if businessOutcome.rpcError != nil {
			return connection.Failure(*businessOutcome.rpcError), nil
		}
		return connection.Success(businessOutcome.value)
	}

	methods.routeMutex.Lock()
	defer methods.routeMutex.Unlock()
	if _, exists := methods.routes[method]; exists {
		return fmt.Errorf("apiproxy: method %q is already registered", method)
	}
	methods.routes[method] = route
	return nil
}

// DecodeObject decodes an object-shaped payload into an owner-defined Go type.
// Unknown keys follow the pinned Zod object behavior and are ignored.
func DecodeObject[P any](rawPayload json.RawMessage) (P, []connection.ValidationIssue) {
	var payload P
	trimmed := bytes.TrimSpace(rawPayload)
	if len(trimmed) < 2 || trimmed[0] != '{' {
		return payload, []connection.ValidationIssue{{
			Code: "invalid_type", Expected: "object", Path: []string{},
			Message: "Invalid input: expected object",
		}}
	}
	if err := json.Unmarshal(trimmed, &payload); err != nil {
		return payload, []connection.ValidationIssue{{
			Code: "invalid_type", Path: []string{}, Message: err.Error(),
		}}
	}
	return payload, nil
}

// HasUnary reports whether the API Proxy owns a method.
func (methods *Catalog) HasUnary(method string) bool {
	if methods == nil {
		return false
	}
	methods.routeMutex.RLock()
	defer methods.routeMutex.RUnlock()
	_, exists := methods.routes[method]
	return exists
}

// DispatchUnary validates and invokes a registered unary method.
func (methods *Catalog) DispatchUnary(requestContext context.Context, method string, rpcID connection.RPCID, rawPayload json.RawMessage) (connection.RPCResult, error) {
	methods.routeMutex.RLock()
	route, exists := methods.routes[method]
	methods.routeMutex.RUnlock()
	if !exists {
		return connection.RPCResult{}, fmt.Errorf("apiproxy: method %q is not registered", method)
	}
	return route(requestContext, rpcID, rawPayload)
}

func invoke[P, V any](operation UnaryHandler[P, V], requestContext context.Context, call Request[P]) (businessOutcome Outcome[V], err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("panic: %v", recovered)
		}
	}()
	return operation(requestContext, call)
}
