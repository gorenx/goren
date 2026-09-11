package sessionadapter

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/google/uuid"
	agentregistry "github.com/gorenx/goren/agent/registry"
	sessionapi "github.com/gorenx/goren/apiproxy/session"
	"github.com/gorenx/goren/session"
	goakt "github.com/tochemey/goakt/v4/actor"
)

// Directory resolves or creates root Agents through the lifecycle Registry.
// It retains immutable composition routes, not a map of live Agent objects.
type Directory struct {
	system   goakt.ActorSystem
	registry *goakt.GrainIdentity
	creation agentregistry.CreateAgent
	timeout  time.Duration
}

// NewDirectory fixes process-level creation dependencies. Request identity,
// recovery choice and Session options always come from the individual API call.
func NewDirectory(system goakt.ActorSystem, registry *goakt.GrainIdentity, creation agentregistry.CreateAgent, timeout time.Duration) (*Directory, error) {
	if system == nil ||
		registry == nil ||
		creation.SessionRegistry == nil ||
		creation.Storage == nil ||
		creation.ModelConfig == nil ||
		creation.PromptRegistry == nil ||
		creation.ToolsRegistry == nil ||
		timeout <= 0 {
		return nil, errors.New("session API: lifecycle dependencies and positive timeout are required")
	}
	creation.Creation = agentregistry.Creation{}
	creation.Parent = nil
	creation.Options = session.CreateOptions{}
	creation.Restore = false
	creation.Components = slices.Clone(creation.Components)
	creation.Timeout = timeout
	return &Directory{
		system:   system,
		registry: registry,
		creation: creation,
		timeout:  timeout,
	}, nil
}

// Resolve waits for an existing initialization; missing or closed Agents return nil.
func (source *Directory) Resolve(ctx context.Context, identifier session.SessionID) (sessionapi.Agent, error) {
	payload, err := source.system.AskGrain(ctx, source.registry, agentregistry.FindAgent{
		AgentID:   identifier,
		WaitReady: true,
	}, source.timeout)
	if err != nil {
		return nil, err
	}
	ready, ok := payload.(agentregistry.Ready)
	if !ok {
		return nil, errors.New("session API: invalid Agent resolution reply")
	}
	if errors.Is(ready.Err, agentregistry.ErrCreation) {
		return nil, nil
	}
	return source.bind(identifier, ready)
}

// Create delegates admission and initialization to Registry. A failed creation
// remains reserved until Registry confirms CloseAgent; a timeout does not cancel it.
// Concurrent callers resolve the winner, then Gateway checks adoption inputs.
func (source *Directory) Create(ctx context.Context, opening sessionapi.Opening) (sessionapi.Agent, error) {
	input := source.creation
	input.Creation = agentregistry.Creation{
		AgentID:    opening.ID,
		CreationID: uuid.NewString(),
	}
	input.Options = opening.Options
	input.Restore = opening.Restore
	payload, err := source.system.AskGrain(ctx, source.registry, input, source.timeout)
	if err != nil {
		return nil, err
	}
	ready, ok := payload.(agentregistry.Ready)
	if !ok {
		return nil, errors.New("session API: invalid Agent creation reply")
	}
	if errors.Is(ready.Err, agentregistry.ErrCreation) {
		winner, failure := source.Resolve(ctx, opening.ID)
		if failure != nil {
			return nil, failure
		}
		if winner != nil {
			return winner, nil
		}
	}
	return source.bind(opening.ID, ready)
}

func (source *Directory) bind(identifier session.SessionID, ready agentregistry.Ready) (sessionapi.Agent, error) {
	if ready.Err != nil {
		return nil, ready.Err
	}
	if ready.Creation.AgentID != identifier ||
		ready.Registration.ID != identifier ||
		ready.Creation.CreationID == "" ||
		ready.Executor == nil {
		return nil, errors.New("session API: Agent resolution identity mismatch")
	}
	return NewAgent(source.system, ready.Executor, ready.Registration, ready.Creation.CreationID, source.timeout)
}

var _ sessionapi.Agents = (*Directory)(nil)
