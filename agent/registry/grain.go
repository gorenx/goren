package registry

import (
	"context"
	"errors"

	"github.com/gorenx/goren/agent"
	agentactor "github.com/gorenx/goren/agent/actor"
	loopactor "github.com/gorenx/goren/agentloop/actor"
	"github.com/gorenx/goren/session"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/reentrancy"
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
	activation *activation
	services   *installation
	releases   map[agent.Resource]releaser
	executor   *goakt.GrainIdentity
	closure    *closure
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
		if entry.activation != nil {
			if entry.activation.cancel != nil {
				entry.activation.cancel()
			}
			for _, reply := range entry.activation.replies {
				reply.Response(Ready{Err: errors.New("agent registry: stopped during creation")})
			}
		}
		if entry.closure != nil {
			entry.closure.respond(errors.New("agent registry: stopped during close"))
		}
	}
	owner.directory = agent.Registry{}
	owner.creations = nil
	return nil
}

// OnReceive dispatches messages to their responsibility owners in this mailbox.
func (owner *Grain) OnReceive(gc *goakt.GrainContext) {
	result, ok := gc.Message().(clientMessage)
	if ok {
		result.deliver(gc)
		return
	}
	if owner.receiveLifecycle(gc) ||
		owner.receiveIdentity(gc) ||
		owner.receiveAdmission(gc) {
		return
	}
	gc.Unhandled()
}

// receiveIdentity registers lifetimes and verifies their authorized activation.
// ReserveAgent must succeed before Claim; neither reply certifies startup readiness.
func (owner *Grain) receiveIdentity(gc *goakt.GrainContext) bool {
	switch input := gc.Message().(type) {
	case ReserveAgent:
		gc.Response(Completion{Err: owner.reserveAgent(input)})
	case agentactor.ClaimTools:
		gc.Response(agentactor.Claimed{Err: owner.claimTools(input)})
	case agentactor.Claim:
		gc.Response(agentactor.Claimed{Err: owner.claim(input)})
	case Inspect:
		err := owner.check(input.Creation)
		if err == nil {
			err = owner.directory.CheckAdmission(input.Creation.AgentID)
		}
		if err == nil && owner.creations[input.Creation.AgentID].instanceID == "" {
			err = ErrInstance
		}
		entry, _ := owner.directory.Get(input.Creation.AgentID)
		gc.Response(Inspected{
			Registration: entry,
			Err:          err,
		})
	default:
		return false
	}
	return true
}

func (owner *Grain) check(identity Creation) error {
	entry, found := owner.creations[identity.AgentID]
	if identity.CreationID == "" || !found ||
		owner.directory.IsClosed(identity.AgentID) ||
		entry.creationID != identity.CreationID {
		return ErrCreation
	}
	return nil
}

func (owner *Grain) reserveAgent(input ReserveAgent) error {
	if input.Creation.AgentID == "" || input.Creation.CreationID == "" {
		return ErrCreation
	}
	previous, found := owner.creations[input.Creation.AgentID]
	if found && owner.directory.IsClosed(input.Creation.AgentID) && previous.creationID == input.Creation.CreationID {
		return ErrCreation
	}
	var parentID *session.SessionID
	var parentCreationID string
	if input.Parent != nil {
		err := owner.check(*input.Parent)
		if err != nil {
			return err
		}
		err = owner.directory.CheckAdmission(input.Parent.AgentID)
		if err != nil {
			return err
		}
		identifier := input.Parent.AgentID
		parentID = &identifier
		parentCreationID = input.Parent.CreationID
	}
	existing, found := owner.creations[input.Creation.AgentID]
	if found && !owner.directory.IsClosed(input.Creation.AgentID) {
		if existing.creationID != input.Creation.CreationID || existing.parentCreationID != parentCreationID {
			return ErrCreation
		}
		registered, _ := owner.directory.Get(input.Creation.AgentID)
		if registered.Role != input.Role {
			return agent.ErrRole
		}
		if (registered.ParentID == nil) != (parentID == nil) || parentID != nil && *registered.ParentID != *parentID {
			return ErrCreation
		}
		return owner.directory.CheckAdmission(input.Creation.AgentID)
	}
	err := owner.directory.Register(agent.Registration{
		Role:     input.Role,
		ID:       input.Creation.AgentID,
		ParentID: parentID,
	})
	if err != nil {
		return err
	}
	if owner.creations == nil {
		owner.creations = make(map[session.SessionID]reservation)
	}
	owner.creations[input.Creation.AgentID] = reservation{
		creationID:       input.Creation.CreationID,
		releases:         make(map[agent.Resource]releaser),
		parentCreationID: parentCreationID,
	}
	if input.Parent != nil {
		parent := owner.creations[input.Parent.AgentID]
		parent.releases[agent.Resource{
			Kind: agent.Child,
			Name: string(input.Creation.AgentID),
		}] = &childClient{creation: input.Creation}
	}

	return nil
}

// claim validates the current reservation and close admission before binding
// its activation. All checks and the binding run in one Registry mailbox turn,
// so a rejected claim leaves the reservation unchanged.
func (owner *Grain) claim(input agentactor.Claim) error {
	err := owner.check(Creation{
		AgentID:    input.AgentID,
		CreationID: input.CreationID,
	})
	if err != nil {
		return err
	}
	err = owner.directory.CheckAdmission(input.AgentID)
	if err != nil {
		return err
	}
	current := owner.creations[input.AgentID]
	if input.InstanceID == "" || input.Executor == nil || input.Executor.Name() != loopactor.RuntimeName(string(input.AgentID)) || current.instanceID != "" && current.instanceID != input.InstanceID || current.executor != nil && !current.executor.Equal(input.Executor) {
		return ErrInstance
	}
	for _, kind := range []agent.ResourceKind{agent.Work, agent.Execution, agent.Activation} {
		err := owner.directory.TrackResource(input.AgentID, agent.Resource{Kind: kind})
		if err != nil {
			return err
		}
	}
	if current.services == nil {
		installExecution(current.releases, &agentClient{
			creation: Creation{
				AgentID:    input.AgentID,
				CreationID: input.CreationID,
			},
			identity: input.Executor,
		})
	}
	current.instanceID = input.InstanceID
	current.executor = input.Executor
	owner.creations[input.AgentID] = current
	return nil
}

// receiveAdmission checks or freezes admission without asserting resource release.
// CloseAgent freezes the subtree before starting asynchronous shutdown steps.
func (owner *Grain) receiveAdmission(gc *goakt.GrainContext) bool {
	switch input := gc.Message().(type) {
	case StopAdmission:
		owner.directory.StopAdmission()
		roots := make([]Creation, 0)
		for _, entry := range owner.directory.List() {
			if entry.ParentID == nil {
				roots = append(roots, Creation{
					AgentID:    entry.ID,
					CreationID: owner.creations[entry.ID].creationID,
				})
			}
		}
		gc.Response(Roots{
			Creations: roots,
		})
	case CheckAdmission:
		err := owner.check(input.Creation)
		if err == nil {
			err = owner.directory.CheckAdmission(input.Creation.AgentID)
		}
		gc.Response(Completion{
			Err: err,
		})
	case BeginClose:
		gc.Response(owner.beginClose(input.Creation))
	default:
		return false
	}
	return true
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

// receiveLifecycle starts or resumes Agent startup and shutdown, or finds a ready
// Agent. Results resume the existing operation; they never create a second one.
func (owner *Grain) receiveLifecycle(gc *goakt.GrainContext) bool {
	switch input := gc.Message().(type) {
	case CreateAgent:
		owner.createAgent(gc, input)
	case CreateChildAgent:
		owner.createChildAgent(gc, input)
	case startupResult:
		owner.handleStartupResult(gc, input)
	case FindAgent:
		owner.findAgent(gc, input)
	case CloseAgent:
		owner.closeAgent(gc, input)
	case shutdownResult:
		owner.handleShutdownResult(gc, input)
	default:
		return false
	}
	return true
}

// deferLifecycle shares the framework setup for asynchronous lifecycle replies.
func deferLifecycle(gc *goakt.GrainContext) (*goakt.GrainReply, error) {
	err := gc.EnableReentrancy(reentrancy.New(reentrancy.WithMode(reentrancy.AllowAll)))
	if err != nil {
		return nil, err
	}
	reply := gc.DeferResponse()
	if reply == nil {
		return nil, errors.New("agent registry: lifecycle operation requires a request")
	}
	return reply, nil
}

func (owner *Grain) claimTools(input agentactor.ClaimTools) error {
	err := owner.check(Creation{
		AgentID:    input.AgentID,
		CreationID: input.CreationID,
	})
	if err != nil {
		return err
	}
	err = owner.directory.CheckAdmission(input.AgentID)
	if err != nil {
		return err
	}
	entry := owner.creations[input.AgentID]
	if entry.services == nil ||
		entry.services.tools == nil ||
		entry.services.tools.Runtime() == nil ||
		input.InstanceID == "" {
		return ErrInstance
	}
	endpoint := entry.services.tools
	if endpoint.instanceID != "" && endpoint.instanceID != input.InstanceID {
		return ErrInstance
	}
	endpoint.instanceID = input.InstanceID
	return nil
}
