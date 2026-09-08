// Package titleregistry owns Title component lifecycle, not title policy.
package titleregistry

import (
	"context"
	"errors"
	"time"

	agentactor "github.com/gorenx/goren/agent/actor"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/session/title"
	goakt "github.com/tochemey/goakt/v4/actor"
)

// Name is the shared Title lifecycle endpoint.
const Name = "titles"

// Configure supplies immutable policy and provider before Agent creation.
type Configure struct {
	Config   title.Config
	Provider title.Provider
	Timeout  time.Duration
}

// Configured acknowledges validated lifecycle configuration.
type Configured struct{ Err error }

type phase uint8

const (
	starting phase = iota
	available
	failed
	stopping
	stopped
)

// instance retains cleanup responsibility, including partial startup failures.
type instance struct {
	input      agentactor.StartComponent
	phase      phase
	endpoint   *goakt.GrainIdentity
	subscriber *goakt.PID
	failure    error
	starters   []*goakt.GrainReply
	stoppers   []*goakt.GrainReply
	cancel     context.CancelFunc
	drained    bool
}

// Grain serializes lifecycle requests from Agent Registry.
type Grain struct {
	settings *Configure
	// instances maps SessionID to its latest component creation, including closed identities.
	instances map[session.SessionID]*instance
}

func (*Grain) OnActivate(context.Context, *goakt.GrainProps) error { return nil }
func (owner *Grain) OnDeactivate(context.Context, *goakt.GrainProps) error {
	for _, entry := range owner.instances {
		if entry.cancel != nil {
			entry.cancel()
		}
		failure := errors.New("title registry: stopped during lifecycle request")
		entry.replyStart(failure)
		entry.replyStop(failure)
	}
	return nil
}

// OnReceive schedules framework work; title decisions remain in title.Policy.
func (owner *Grain) OnReceive(gc *goakt.GrainContext) {
	switch input := gc.Message().(type) {
	case Configure:
		var failure error
		if owner.settings != nil || input.Timeout <= 0 {
			failure = errors.New("title registry: fresh configuration and positive timeout required")
		}
		if failure == nil {
			_, failure = title.NewPolicy(input.Config)
		}
		if failure == nil {
			owner.settings = &input
		}
		gc.Response(Configured{
			Err: failure,
		})
	case agentactor.StartComponent:
		owner.start(gc, input)
	case initialized:
		owner.finishStart(gc, input)
	case agentactor.StopComponent:
		owner.stop(gc, input)
	case closed:
		owner.finishStop(gc, input)
	default:
		gc.Unhandled()
	}
}

func (entry *instance) replyStart(failure error) {
	result := agentactor.ComponentStarted{
		Err: failure,
	}
	if failure == nil {
		result.Runtime = entry.endpoint
	}
	for _, reply := range entry.starters {
		reply.Response(result)
	}
	entry.starters = nil
}
func (entry *instance) replyStop(failure error) {
	for _, reply := range entry.stoppers {
		reply.Response(agentactor.ComponentStopped{
			Err: failure,
		})
	}
	entry.stoppers = nil
}
