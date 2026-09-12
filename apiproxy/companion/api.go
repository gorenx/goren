// Package companionapi owns the transport-neutral Companion management contract.
package companionapi

import (
	"context"
	"errors"

	"github.com/gorenx/goren/apiproxy/rpc"
	"github.com/gorenx/goren/session"
	companions "github.com/gorenx/goren/subagent/companion"
)

const (
	ListMethod      = "companion.list"
	CreateMethod    = "companion.create"
	ConfigureMethod = "companion.configure"
	RemoveMethod    = "companion.remove"
	RestartMethod   = "companion.restart"
)

// ListRequest selects one current parent's Companion manager.
type ListRequest struct {
	ParentSessionID session.SessionID `json:"parentSessionId"`
}

// CreateRequest selects a named policy or supplies one complete instance policy.
type CreateRequest struct {
	ParentSessionID session.SessionID  `json:"parentSessionId"`
	Name            string             `json:"name"`
	Config          *companions.Config `json:"config,omitempty"`
}

// ConfigureRequest changes the enabled target at an observed revision.
type ConfigureRequest struct {
	ParentSessionID  session.SessionID `json:"parentSessionId"`
	Name             string            `json:"name"`
	ExpectedRevision int64             `json:"expectedRevision"`
	Enabled          bool              `json:"enabled"`
}

// RemoveRequest preserves a tombstone at an observed revision.
type RemoveRequest struct {
	ParentSessionID  session.SessionID `json:"parentSessionId"`
	Name             string            `json:"name"`
	ExpectedRevision int64             `json:"expectedRevision"`
}

// RestartRequest fences a manual restart by relationship and child lifetime.
// Revision zero and an empty creation ID identify failed initial preparation.
type RestartRequest struct {
	ParentSessionID    session.SessionID `json:"parentSessionId"`
	Name               string            `json:"name"`
	ExpectedRevision   int64             `json:"expectedRevision"`
	ExpectedCreationID string            `json:"expectedCreationId"`
}

// View contains the durable target and current phase without private errors.
type View struct {
	// Activity is idle, running, or unavailable for a lifecycle-ready child.
	Activity string `json:"activity,omitempty"`
	// Configuration identifies the independent Settings entry used by this Companion.
	Configuration   string            `json:"configuration,omitempty"`
	ParentSessionID session.SessionID `json:"parentSessionId"`
	Name            string            `json:"name"`
	ChildSessionID  session.SessionID `json:"childSessionId,omitempty"`
	Revision        int64             `json:"revision"`
	SourceRevision  int64             `json:"sourceRevision"`
	CreationID      string            `json:"creationId,omitempty"`
	Enabled         bool              `json:"enabled"`
	Removed         bool              `json:"removed"`
	Persisted       bool              `json:"persisted"`
	Phase           string            `json:"phase"`
	Failure         bool              `json:"failure"`
}

// Listing includes manager readiness independently of child phases.
type Listing struct {
	Phase   string `json:"phase"`
	Failure bool   `json:"failure"`
	Items   []View `json:"items"`
}

// Manager is the consumer-owned boundary to a current parent Steward.
type Manager interface {
	List(context.Context, ListRequest) (Listing, error)
	Create(context.Context, CreateRequest) (View, error)
	Configure(context.Context, ConfigureRequest) (View, error)
	Remove(context.Context, RemoveRequest) (View, error)
	Restart(context.Context, RestartRequest) (View, error)
}

// ErrNotFound means the selected named creation policy is absent.
var ErrNotFound = errors.New("companion API: named policy not found")

// Register installs the management methods in the existing RPC catalog.
func Register(methods *rpc.Catalog, source Manager) error {
	if source == nil {
		return errors.New("companion API: manager is required")
	}
	gateway := &Gateway{
		manager: source,
	}
	if err := rpc.RegisterUnary(methods, ListMethod, DecodeListRequest, gateway.List); err != nil {
		return err
	}
	if err := rpc.RegisterUnary(methods, CreateMethod, DecodeCreateRequest, gateway.Create); err != nil {
		return err
	}
	if err := rpc.RegisterUnary(methods, ConfigureMethod, DecodeConfigureRequest, gateway.Configure); err != nil {
		return err
	}
	if err := rpc.RegisterUnary(methods, RemoveMethod, DecodeRemoveRequest, gateway.Remove); err != nil {
		return err
	}
	return rpc.RegisterUnary(methods, RestartMethod, DecodeRestartRequest, gateway.Restart)
}
