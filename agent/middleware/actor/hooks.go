package middlewareactor

import (
	"errors"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/llm"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/reentrancy"
)

// HookCompleted delivers a correlated business completion to the calling Grain.
// The receiver must match CallID against its current invocation before applying it.
type HookCompleted struct {
	CallID CallID
	Result agent.HookResult
}

// Hooks binds one invocation to the current receive frame. It must be used
// before OnReceive returns and must not be retained by the business owner.
// Dispatch consumes the frame reference; asynchronous closures never capture it.
type Hooks struct {
	scope    *goakt.GrainContext
	target   *goakt.GrainIdentity
	identity CallID
	routes   []Route
}

var _ agent.Hooks = (*Hooks)(nil)

// NewHooks binds the fixed middleware selection for one extension invocation.
// The caller supplies a fresh identity and the routes for the chosen hook only.
func NewHooks(scope *goakt.GrainContext, target *goakt.GrainIdentity, identity CallID, routes []Route) (*Hooks, error) {
	if scope == nil || target == nil || identity == "" {
		return nil, errors.New("agent middleware: hook invocation requires a frame, coordinator and identity")
	}
	if err := target.Validate(); err != nil {
		return nil, err
	}
	if _, _, err := selectRoutes(routes); err != nil {
		return nil, err
	}
	return &Hooks{
		scope:    scope,
		target:   target,
		identity: identity,
		routes:   append([]Route(nil), routes...),
	}, nil
}

// PreStep starts admission and later delivers agent.PreStepResolved.
func (sender *Hooks) PreStep(input agent.PreStepInput, fallback agent.PreStepDecision) error {
	return sender.dispatch(ResolvePreStep{
		CallID:  sender.identity,
		Routes:  sender.routes,
		Input:   input,
		Default: fallback,
	}, func(payload any, failure error) agent.HookResult {
		result, ok := payload.(agent.PreStepResolved)
		if failure != nil || !ok {
			return agent.PreStepResolved{
				Err: responseError(failure),
			}
		}
		return result
	})
}

// Request starts configuration resolution and later delivers agent.RequestResolved.
func (sender *Hooks) Request(input agent.RequestInput, fallback llm.CallConfig) error {
	return sender.dispatch(ResolveRequest{
		CallID:  sender.identity,
		Routes:  sender.routes,
		Input:   input,
		Default: fallback,
	}, func(payload any, failure error) agent.HookResult {
		result, ok := payload.(agent.RequestResolved)
		if failure != nil || !ok {
			return agent.RequestResolved{
				Err: responseError(failure),
			}
		}
		return result
	})
}

// RequestError starts recovery and later delivers agent.RequestErrorResolved.
func (sender *Hooks) RequestError(input agent.RequestFailure) error {
	return sender.dispatch(ResolveRequestError{
		CallID: sender.identity,
		Routes: sender.routes,
		Input:  input,
	}, func(payload any, failure error) agent.HookResult {
		result, ok := payload.(agent.RequestErrorResolved)
		if failure != nil || !ok {
			return agent.RequestErrorResolved{
				Err: responseError(failure),
			}
		}
		return result
	})
}

// TurnStopping starts ordered hooks and later delivers agent.TurnStoppingCompleted.
func (sender *Hooks) TurnStopping(input agent.TurnStoppingInput) error {
	return sender.dispatch(RunTurnStopping{
		CallID: sender.identity,
		Routes: sender.routes,
		Input:  input,
	}, func(payload any, failure error) agent.HookResult {
		result, ok := payload.(agent.TurnStoppingCompleted)
		if failure != nil || !ok {
			return agent.TurnStoppingCompleted{
				Err: responseError(failure),
			}
		}
		return result
	})
}

// dispatch confines framework erasure and the stateless decoder to this adapter.
func (sender *Hooks) dispatch(message any, decode func(any, error) agent.HookResult) error {
	if sender.scope == nil {
		return errors.New("agent middleware: hook invocation was already dispatched")
	}
	scope := sender.scope
	sender.scope = nil
	identity := sender.identity
	completed := make(chan HookCompleted, 1)
	if err := scope.PipeToSelf(func() (any, error) {
		return <-completed, nil
	}); err != nil {
		return err
	}
	scope.RequestGrain(sender.target, message,
		goakt.WithRequestTimeout(0),
		goakt.WithReentrancyMode(reentrancy.AllowAll),
	).Then(func(payload any, failure error) {
		completed <- HookCompleted{
			CallID: identity,
			Result: decode(payload, failure),
		}
	})
	return nil
}

func responseError(failure error) error {
	if failure != nil {
		return failure
	}
	return errors.New("agent middleware: unexpected hook response")
}
