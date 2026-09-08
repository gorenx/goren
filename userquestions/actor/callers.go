package questionactor

import (
	"context"
	"errors"
	"time"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/agent/registry"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/userquestions"
	goakt "github.com/tochemey/goakt/v4/actor"
)

// Callers binds interaction admission to one reserved Agent lifetime. It runs
// in the provider task, never synchronously inside a Grain dispatcher callback.
type Callers struct {
	system    goakt.ActorSystem
	directory *goakt.GrainIdentity
	creation  registry.Creation
	timeout   time.Duration
}

// NewCallers binds an exact Agent reservation for runtime-root admission.
func NewCallers(system goakt.ActorSystem, directory *goakt.GrainIdentity, creation registry.Creation, timeout time.Duration) (*Callers, error) {
	if system == nil || directory == nil || creation.AgentID == "" || creation.CreationID == "" || timeout <= 0 {
		return nil, errors.New("userquestions: caller authority requires an Agent reservation and endpoint")
	}
	return &Callers{
		system:    system,
		directory: directory,
		creation:  creation,
		timeout:   timeout,
	}, nil
}

// CheckCaller rejects a replaced lifetime, closing ancestor or different Session.
func (source *Callers) CheckCaller(ctx context.Context, id session.SessionID) (userquestions.Eligibility, error) {
	if id != source.creation.AgentID {
		return userquestions.Eligibility{}, nil
	}
	payload, err := source.system.AskGrain(ctx, source.directory, registry.Inspect{
		Creation: source.creation,
	}, source.timeout)
	if err != nil {
		return userquestions.Eligibility{}, err
	}
	result, ok := payload.(registry.Inspected)
	if !ok {
		return userquestions.Eligibility{}, errors.New("userquestions: invalid caller admission reply")
	}
	if errors.Is(result.Err, registry.ErrCreation) || errors.Is(result.Err, registry.ErrInstance) || errors.Is(result.Err, agent.ErrClosing) {
		return userquestions.Eligibility{}, nil
	}
	if result.Err != nil {
		return userquestions.Eligibility{}, result.Err
	}
	return userquestions.Eligibility{
		Live:      true,
		Delegated: result.Registration.ParentID != nil,
	}, nil
}
