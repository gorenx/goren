// Package companionactor adapts Companion management to one Grain per parent.
package companionactor

import (
	"context"
	"errors"
	"time"

	agentregistry "github.com/gorenx/goren/agent/registry"
	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/settings"
	"github.com/gorenx/goren/subagent/companion"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/reentrancy"
)

// ChildCreator enters the ordinary Agent/Session creation and recovery workflow.
// Start returns only after the admitted creation request settles. Errors retain
// cleanup responsibility in Registry; cancellation must not abandon that request.
// The configuration name is a Settings reference, independent of Session identity.
type ChildCreator interface {
	Start(context.Context, agentregistry.Creation, agentregistry.Creation, string) (agentregistry.Ready, error)
}

// Initialization binds one parent lifetime and its existing capabilities.
type Initialization struct {
	Parent   agentregistry.Creation
	Registry *goakt.GrainIdentity
	Session  *goakt.GrainIdentity
	Creator  ChildCreator
	Settings settings.Settings
}

// Create fixes a new child's resolved first-creation configuration.
type Create struct {
	Name           string
	SourceRevision int64
	Enabled        bool
	Initial        settings.Values
}

// Configure conditionally changes whether an existing Companion is enabled.
type Configure struct {
	Name             string
	ExpectedRevision int64
	Enabled          bool
}

// Remove preserves a tombstone while closing the current Companion.
type Remove struct {
	Name             string
	ExpectedRevision int64
}

// Restart rejects a stale management version or running lifetime.
type Restart struct {
	Name               string
	ExpectedRevision   int64
	ExpectedCreationID string
}

// Phase describes a Companion's current execution and cleanup progress.
type Phase string

// Companion lifecycle phases are also used in management query results.
const (
	Starting    Phase = "starting"
	Ready       Phase = "ready"
	Draining    Phase = "draining"
	Stopping    Phase = "stopping"
	Stopped     Phase = "stopped"
	StartFailed Phase = "start_failed"
	CloseFailed Phase = "close_failed"
)

// View separates durable management facts from runtime lifecycle progress.
// Err is an internal failure, not a public wire representation.
type View struct {
	// Persisted is false while the first relationship write is unconfirmed.
	Persisted bool
	Companion companion.Companion
	Creation  agentregistry.Creation
	Phase     Phase
	Err       error
}

// Client addresses a Steward with a fixed parent lifetime credential.
type Client struct {
	system goakt.ActorSystem
	target *goakt.GrainIdentity
	parent agentregistry.Creation
}

type command struct {
	parent agentregistry.Creation
	input  any
}

type listing struct{}
type closing struct{}
type response struct {
	view      View
	items     []View
	messageID agentmessage.MessageID
	err       error
}

// Open initializes management and recovers durable relationships. It does not
// wait for child initialization. The parent must already be a ready ordinary Agent.
func Open(ctx context.Context, system goakt.ActorSystem, input Initialization) (*Client, error) {
	if ctx == nil || system == nil || input.Registry == nil || input.Session == nil || input.Creator == nil || input.Settings == nil || input.Parent.AgentID == "" || input.Parent.CreationID == "" {
		return nil, companion.ErrInvalid
	}
	target, err := stewardRoute(ctx, system, input.Parent)
	if err != nil {
		return nil, err
	}
	result, err := ask[response](ctx, system, target, input)
	if err != nil {
		return nil, err
	}
	if result.err != nil {
		return nil, result.err
	}
	return &Client{
		system: system,
		target: target,
		parent: input.Parent,
	}, nil
}

func stewardRoute(ctx context.Context, system goakt.ActorSystem, parent agentregistry.Creation) (*goakt.GrainIdentity, error) {
	return goakt.GrainOf[*steward](ctx, system, string(parent.AgentID)+".steward",
		goakt.WithLongLivedGrain(),
		goakt.WithGrainReentrancy(reentrancy.New(reentrancy.WithMode(reentrancy.AllowAll))),
	)
}

// List reads management progress without creating children.
func (sender *Client) List(ctx context.Context) ([]View, error) {
	result, err := sender.request(ctx, listing{})
	return result.items, errors.Join(err, result.err)
}

// Create persists the intent before starting any child resources.
func (sender *Client) Create(ctx context.Context, input Create) (View, error) {
	input.Initial = input.Initial.Clone()
	result, err := sender.request(ctx, input)
	return result.view, errors.Join(err, result.err)
}

// Configure changes enablement without waiting for child startup or shutdown.
func (sender *Client) Configure(ctx context.Context, input Configure) (View, error) {
	result, err := sender.request(ctx, input)
	return result.view, errors.Join(err, result.err)
}

// Remove stops new tasks and allows active work to finish before closing.
func (sender *Client) Remove(ctx context.Context, input Remove) (View, error) {
	result, err := sender.request(ctx, input)
	return result.view, errors.Join(err, result.err)
}

// Restart closes the previous lifetime before starting the same child Session.
func (sender *Client) Restart(ctx context.Context, input Restart) (View, error) {
	result, err := sender.request(ctx, input)
	return result.view, errors.Join(err, result.err)
}

// Close settles all children and in-flight management work. It is retryable;
// a failure never authorizes discarding the Registry's remaining resource ledger.
func (sender *Client) Close(ctx context.Context) error {
	result, err := sender.request(ctx, closing{})
	return errors.Join(err, result.err)
}

func (sender *Client) request(ctx context.Context, input any) (response, error) {
	if ctx == nil || sender == nil {
		return response{}, companion.ErrInvalid
	}
	return ask[response](ctx, sender.system, sender.target, command{
		parent: sender.parent,
		input:  input,
	})
}

func ask[T any](ctx context.Context, system goakt.ActorSystem, target *goakt.GrainIdentity, input any) (T, error) {
	var empty T
	payload, err := system.AskGrain(ctx, target, input, time.Duration(1<<63-1))
	if err != nil {
		return empty, err
	}
	result, ok := payload.(T)
	if !ok {
		return empty, errors.New("companion: unexpected capability response")
	}
	return result, nil
}
