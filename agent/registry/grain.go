package registry

import (
	"context"
	"errors"

	"github.com/gorenx/goren/agent"
	agentactor "github.com/gorenx/goren/agent/actor"
	"github.com/gorenx/goren/session"
	goakt "github.com/tochemey/goakt/v4/actor"
)

// Name is the shared Agent Registry Grain name.
const Name = "agent-registry"

// ErrCreation rejects operations from a missing or superseded creation.
var ErrCreation = errors.New("agent registry: creation is not current")

// ErrInstance rejects an empty or conflicting AgentLoop activation identity.
var ErrInstance = errors.New("agent registry: activation is not authorized")

type reservation struct {
	creationID       string
	parentCreationID string
	// instanceID is fixed by the first Claim for this creation. Empty is unclaimed.
	// It protects execution ownership without duplicating the framework address map.
	instanceID string
	executor   *goakt.GrainIdentity
	closure    *closure
	// closed retains the last successful close until a fresh reservation replaces it.
	closed bool
}

// Grain owns Agent identity, admission and descendant close coordination.
// Registry primitives acknowledge only their own operation; CloseAgent waits
// for resource shutdown and framework deactivation before releasing identity.
type Grain struct {
	directory agent.Registry
	// creations maps Agent IDs to their latest reservation and close progress.
	// Completed entries retain an idempotent close result until the next creation.
	// Parent Agent IDs live in the business directory.
	creations map[session.SessionID]reservation
}

// OnActivate begins without admitting previous process-local creations.
func (*Grain) OnActivate(context.Context, *goakt.GrainProps) error { return nil }

// OnDeactivate discards admission state, never asserting resource shutdown.
func (owner *Grain) OnDeactivate(context.Context, *goakt.GrainProps) error {
	for _, entry := range owner.creations {
		if entry.closure != nil {
			entry.closure.respond(errors.New("agent registry: stopped during close"))
		}
	}
	owner.directory = agent.Registry{}
	owner.creations = nil
	return nil
}

// OnReceive serializes identity decisions and resumes asynchronous close steps.
func (owner *Grain) OnReceive(gc *goakt.GrainContext) {
	var err error
	switch input := gc.Message().(type) {
	case CloseAgent:
		owner.closeAgent(gc, input)
		return
	case closeAdvanced:
		owner.advanceClose(gc, input)
		return
	case Reserve:
		err = owner.reserve(input)
	case agentactor.Claim:
		gc.Response(agentactor.Claimed{
			Err: owner.claim(input),
		})
		return
	case CheckAdmission:
		err = owner.check(input.Creation)
		if err == nil {
			err = owner.directory.CheckAdmission(input.Creation.AgentID)
		}
	case BeginClose:
		gc.Response(owner.beginClose(input.Creation))
		return
	case ReleaseReservation:
		err = owner.check(input.Creation)
		if err == nil && owner.creations[input.Creation.AgentID].closure != nil {
			err = errors.New("agent registry: close coordination owns this reservation")
		}
		if err == nil {
			err = owner.directory.Remove(input.Creation.AgentID)
			if err == nil {
				delete(owner.creations, input.Creation.AgentID)
			}
		}
	default:
		gc.Unhandled()
		return
	}
	gc.Response(Completion{
		Err: err,
	})
}

// beginClose freezes admission before reading child creations in the same
// mailbox turn. A retry returns only children whose reservations still exist.
func (owner *Grain) beginClose(identity Creation) CloseAccepted {
	if err := owner.check(identity); err != nil {
		return CloseAccepted{
			Err: err,
		}
	}
	if err := owner.directory.BeginClose(identity.AgentID); err != nil {
		return CloseAccepted{
			Err: err,
		}
	}
	identifiers := owner.directory.Children(identity.AgentID)
	children := make([]Creation, 0, len(identifiers))
	for _, identifier := range identifiers {
		children = append(children, Creation{
			AgentID:    identifier,
			CreationID: owner.creations[identifier].creationID,
		})
	}
	return CloseAccepted{
		Children: children,
	}
}

func (owner *Grain) check(identity Creation) error {
	entry, found := owner.creations[identity.AgentID]
	if identity.CreationID == "" || !found || entry.closed || entry.creationID != identity.CreationID {
		return ErrCreation
	}
	return nil
}

func (owner *Grain) reserve(input Reserve) error {
	if input.Creation.AgentID == "" || input.Creation.CreationID == "" {
		return ErrCreation
	}
	if previous, found := owner.creations[input.Creation.AgentID]; found && previous.closed && previous.creationID == input.Creation.CreationID {
		return ErrCreation
	}
	var parentID *session.SessionID
	var parentCreationID string
	if input.Parent != nil {
		if err := owner.check(*input.Parent); err != nil {
			return err
		}
		if err := owner.directory.CheckAdmission(input.Parent.AgentID); err != nil {
			return err
		}
		identifier := input.Parent.AgentID
		parentID = &identifier
		parentCreationID = input.Parent.CreationID
	}
	if existing, found := owner.creations[input.Creation.AgentID]; found && !existing.closed {
		if existing.creationID != input.Creation.CreationID || existing.parentCreationID != parentCreationID {
			return ErrCreation
		}
		registered, _ := owner.directory.Get(input.Creation.AgentID)
		if (registered.ParentID == nil) != (parentID == nil) || parentID != nil && *registered.ParentID != *parentID {
			return ErrCreation
		}
		return owner.directory.CheckAdmission(input.Creation.AgentID)
	}
	if err := owner.directory.Register(agent.Registration{
		ID:       input.Creation.AgentID,
		ParentID: parentID,
	}); err != nil {
		return err
	}
	if owner.creations == nil {
		owner.creations = make(map[session.SessionID]reservation)
	}
	owner.creations[input.Creation.AgentID] = reservation{
		creationID:       input.Creation.CreationID,
		parentCreationID: parentCreationID,
	}
	return nil
}

// claim validates the current reservation and close admission before binding
// its activation. All checks and the binding run in one Registry mailbox turn,
// so a rejected claim leaves the reservation unchanged.
func (owner *Grain) claim(input agentactor.Claim) error {
	if err := owner.check(Creation{
		AgentID:    input.AgentID,
		CreationID: input.CreationID,
	}); err != nil {
		return err
	}
	if err := owner.directory.CheckAdmission(input.AgentID); err != nil {
		return err
	}
	current := owner.creations[input.AgentID]
	if input.InstanceID == "" || input.Executor == nil || input.Executor.Name() != string(input.AgentID) || current.instanceID != "" && current.instanceID != input.InstanceID || current.executor != nil && !current.executor.Equal(input.Executor) {
		return ErrInstance
	}
	current.instanceID = input.InstanceID
	current.executor = input.Executor
	owner.creations[input.AgentID] = current
	return nil
}
