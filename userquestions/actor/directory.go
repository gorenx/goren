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

// Directory checks the current caller for a process-wide question capability.
// The invoking Tool Runtime retains its Agent lifetime until the question exits.
type Directory struct {
	system  goakt.ActorSystem
	target  *goakt.GrainIdentity
	timeout time.Duration
}

// NewDirectory binds current-Agent discovery without caching Session ancestry.
func NewDirectory(system goakt.ActorSystem, target *goakt.GrainIdentity, timeout time.Duration) (*Directory, error) {
	if system == nil || target == nil || timeout <= 0 {
		return nil, errors.New("userquestions: Agent Registry and timeout are required")
	}
	return &Directory{
		system:  system,
		target:  target,
		timeout: timeout,
	}, nil
}

// CheckCaller selects and then inspects one exact currently ready Agent lifetime.
func (source *Directory) CheckCaller(ctx context.Context, id session.SessionID) (userquestions.Eligibility, error) {
	payload, err := source.system.AskGrain(ctx, source.target, registry.FindAgent{
		AgentID: id,
	}, source.timeout)
	if err != nil {
		return userquestions.Eligibility{}, err
	}
	found, valid := payload.(registry.Ready)
	if !valid {
		return userquestions.Eligibility{}, errors.New("userquestions: invalid Agent lookup reply")
	}
	if errors.Is(found.Err, registry.ErrCreation) ||
		errors.Is(found.Err, registry.ErrInstance) ||
		errors.Is(found.Err, agent.ErrClosing) {
		return userquestions.Eligibility{}, nil
	}
	if found.Err != nil {
		return userquestions.Eligibility{}, found.Err
	}
	if found.Creation.AgentID != id {
		return userquestions.Eligibility{}, errors.New("userquestions: Agent lookup identity mismatch")
	}
	authority, err := NewCallers(source.system, source.target, found.Creation, source.timeout)
	if err != nil {
		return userquestions.Eligibility{}, err
	}
	return authority.CheckCaller(ctx, id)
}

var _ userquestions.Callers = (*Directory)(nil)
