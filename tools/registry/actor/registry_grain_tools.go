package toolactor

import (
	"errors"

	"github.com/gorenx/goren/session"
	goakt "github.com/tochemey/goakt/v4/actor"
	"strings"
)

// RegisterTool publishes a callable Actor under its tool name. Empty AgentID
// registers a global tool; an Agent-specific name takes precedence at lookup.
type RegisterTool struct {
	AgentID    session.SessionID
	Name       string
	CreationID string
	Actor      *goakt.GrainIdentity
}

// UnregisterTool withdraws only the named Actor lifetime that registered it.
type UnregisterTool struct {
	AgentID    session.SessionID
	Name       string
	CreationID string
}

// FindTool looks up an Agent override, then the global callable tool.
type FindTool struct {
	AgentID session.SessionID
	Name    string
}

// ToolAddress is routing data, never a tool implementation or executor interface.
type ToolAddress struct {
	Actor      *goakt.GrainIdentity
	CreationID string
	Found      bool
	Err        error
}

type toolName struct {
	agentID session.SessionID
	name    string
}

type address struct {
	actor      *goakt.GrainIdentity
	creationID string
}

func (owner *RegistryGrain) receiveTools(gc *goakt.GrainContext) bool {
	switch input := gc.Message().(type) {
	case RegisterTool:
		gc.Response(Completion{
			Err: owner.registerTool(input),
		})
	case UnregisterTool:
		key := toolName{
			agentID: input.AgentID,
			name:    input.Name,
		}
		current, found := owner.actors[key]
		if found && current.creationID == input.CreationID {
			delete(owner.actors, key)
		}
		gc.Response(Completion{})
	case FindTool:
		key := toolName{
			agentID: input.AgentID,
			name:    input.Name,
		}
		current, found := owner.actors[key]
		if !found && input.AgentID != "" {
			key.agentID = ""
			current, found = owner.actors[key]
		}
		gc.Response(ToolAddress{
			Actor:      current.actor,
			CreationID: current.creationID,
			Found:      found,
		})
	default:
		return false
	}
	return true
}

func (owner *RegistryGrain) registerTool(input RegisterTool) error {
	if input.Actor == nil ||
		strings.TrimSpace(input.Name) == "" ||
		input.Name != strings.TrimSpace(input.Name) ||
		input.CreationID == "" {
		return errors.New("tools: name, lifetime and Actor address are required")
	}
	err := input.Actor.Validate()
	if err != nil {
		return err
	}
	key := toolName{
		agentID: input.AgentID,
		name:    input.Name,
	}
	previous, found := owner.actors[key]
	if found {
		if previous.creationID == input.CreationID && previous.actor.Equal(input.Actor) {
			return nil
		}
		return errors.New("tools: tool name is registered by another Actor lifetime")
	}
	if owner.actors == nil {
		owner.actors = make(map[toolName]address)
	}
	owner.actors[key] = address{
		actor:      input.Actor,
		creationID: input.CreationID,
	}
	return nil
}
