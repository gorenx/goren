// Package companionactor adapts Companion management to one Grain per parent.
package companionactor

import (
	"context"
	"errors"
	"time"

	"github.com/gorenx/goren/agent"
	agentregistry "github.com/gorenx/goren/agent/registry"
	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/settings"
	companions "github.com/gorenx/goren/subagent/companion"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/reentrancy"
)

// ChildCreator enters the ordinary Agent/Session creation and recovery workflow.
// Start returns only after the admitted creation request settles. Errors retain
// cleanup responsibility in Registry; cancellation must not abandon that request.
// The configuration name is a Settings reference, independent of Session identity.
type ChildCreator interface {
	Start(context.Context, agentregistry.Creation, agentregistry.Creation, string) (agentregistry.Ready, error)
	Resolve(context.Context, agentregistry.Creation, companions.Config, settings.Values) (settings.Values, error)
}

// start accepts a parent lifetime and recovers its relationships asynchronously.
type start struct {
	// Parent identifies the exact lifetime checked by subsequent management commands.
	Parent agentregistry.Creation
	// Registry is the Agent Registry endpoint for identity checks, readiness and child closure.
	Registry *goakt.GrainIdentity
	// Session is the parent Session endpoint that reads and commits Companion relationships.
	Session *goakt.GrainIdentity
	// Creator borrows ordinary child creation; admitted requests must settle before returning.
	Creator ChildCreator
	// Settings owns named configurations and their persistence.
	Settings settings.Settings
}

// Create fixes a new child's resolved first-creation configuration.
type Create struct {
	// Name uniquely identifies the Companion within the current parent.
	Name string
	// SourceRevision identifies the selected creation policy, separately from relationship revision.
	SourceRevision int64
	// Enabled is the initial target; child startup requires confirmed relationship persistence.
	Enabled bool
	// Initial contains resolved explicit inheritance; the client freezes it before Settings persistence.
	Initial settings.Values
}

// Configure conditionally changes whether an existing Companion is enabled.
type Configure struct {
	// Name selects the Companion whose enablement target changes.
	Name string
	// ExpectedRevision rejects changes based on an outdated relationship.
	ExpectedRevision int64
	// Enabled is the new target; disabling rejects new work and drains current work.
	Enabled bool
}

// Remove preserves a tombstone while closing the current Companion.
type Remove struct {
	// Name selects the Companion to remove from this parent.
	Name string
	// ExpectedRevision must match the current relationship; removal remains in its log.
	ExpectedRevision int64
}

// Restart rejects a stale management version or running lifetime.
type Restart struct {
	// Name selects the Companion to restart within this parent.
	Name string
	// ExpectedRevision identifies the relationship on which this restart is based.
	ExpectedRevision int64
	// ExpectedCreationID is the observed child lifetime, or empty when none is retained.
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

// Status reports Steward preparation and shutdown independently of parent readiness.
type Status struct {
	// Phase describes Steward recovery or Companion support shutdown, independently of parent readiness.
	Phase Phase
	// Err retains the phase failure and can be non-nil even when the status query succeeds.
	Err error
}

// View separates durable management facts from runtime lifecycle progress.
// Err is an internal failure, not a public wire representation.
type View struct {
	// Persisted confirms Session Flush; it is false before the first relationship write is confirmed.
	Persisted bool
	// Companion is a detached relationship view and may contain the uncommitted first target.
	Companion companions.Companion
	// Creation is the current or unsettled child lifetime; empty means no lifetime is retained.
	Creation agentregistry.Creation
	// Phase describes child startup and closure, not whether its current Turn is busy.
	Phase Phase
	// Err contains management or child lifecycle failures for internal queries and boundary mapping.
	Err error
}

// Client addresses a Steward with a fixed parent lifetime credential.
type Client struct {
	// system is borrowed for messaging through GoAkt Grain routes.
	system goakt.ActorSystem
	// target addresses this parent's Steward Grain.
	target *goakt.GrainIdentity
	// parent fixes the lifetime credential; a replacement Creation cannot reuse it.
	parent agentregistry.Creation
}

// OpenClient resolves the currently installed Steward through the parent's
// ordinary component routes. It never creates a route for a missing parent.
func OpenClient(
	ctx context.Context,
	system goakt.ActorSystem,
	registry *goakt.GrainIdentity,
	parentID session.SessionID,
) (*Client, error) {
	if ctx == nil || system == nil || registry == nil || parentID == "" {
		return nil, companions.ErrInvalid
	}
	ready, err := ask[agentregistry.Ready](ctx, system, registry, agentregistry.FindAgent{
		AgentID:   parentID,
		WaitReady: true,
	})
	if err != nil || ready.Err != nil {
		return nil, errors.Join(err, ready.Err)
	}
	if ready.Creation.AgentID != parentID || ready.Creation.CreationID == "" {
		return nil, agentregistry.ErrCreation
	}
	if ready.Registration.Role == agent.CompanionRole {
		return nil, agent.ErrRole
	}
	for _, component := range ready.Components {
		if component.Owner != nil && component.Owner.Name() == InstallerName && component.Runtime != nil {
			return &Client{
				system: system,
				target: component.Runtime,
				parent: ready.Creation,
			}, nil
		}
	}
	return nil, agentregistry.ErrCreation
}

// Creation returns the exact parent lifetime retained by this client.
func (sender *Client) Creation() agentregistry.Creation { return sender.parent }

type command struct {
	// parent carries the exact lifetime checked before business dispatch.
	parent agentregistry.Creation
	// input holds a management, query or delivery message; type erasure stays at the Grain boundary.
	input any
}

type listing struct{}
type inspection struct{}
type closing struct{}
type response struct {
	// status contains recovery or shutdown progress and any retained lifecycle failure.
	status Status
	// view is the current Companion state returned by a single management command.
	view View
	// items contains the Companion views returned by a listing query.
	items []View
	// messageID identifies input accepted by the ordinary Inbox, not completed execution.
	messageID agentmessage.MessageID
	// err reports command failure; lifecycle failures from a status query belong to status.Err.
	err error
}

// Status reads the manager's current preparation or shutdown result.
func (sender *Client) Status(ctx context.Context) (Status, error) {
	result, err := sender.request(ctx, inspection{})
	if err != nil {
		return Status{}, err
	}
	return result.status, result.err
}

func stewardRoute(
	ctx context.Context,
	system goakt.ActorSystem,
	parent agentregistry.Creation,
) (*goakt.GrainIdentity, error) {
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
		return response{}, companions.ErrInvalid
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
