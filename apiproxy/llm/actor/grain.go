package apiactor

import (
	"context"
	"errors"
	llmactor "github.com/gorenx/goren/llm/actor"
	"time"

	goakt "github.com/tochemey/goakt/v4/actor"
)

// Initialize binds the request bridge before it accepts metadata queries.
type Initialize struct {
	Router *goakt.PID
}

// Grain bridges deferred Grain requests to explicit Router result messages.
// It owns correlation only; Worker and Binding retain all query behavior.
type Grain struct {
	router  *goakt.PID
	pending map[llmactor.RequestID]queryReply
	stopped chan struct{}
}

type queryReply struct {
	reply   *goakt.GrainReply
	cancel  context.CancelFunc
	request any
}

type queryExpired struct {
	id    llmactor.RequestID
	cause error
}

// OnActivate creates an empty transport correlation table.
func (owner *Grain) OnActivate(context.Context, *goakt.GrainProps) error {
	owner.pending = make(map[llmactor.RequestID]queryReply)
	owner.stopped = make(chan struct{})
	return nil
}

// OnDeactivate fails every outstanding caller instead of losing its reply.
func (owner *Grain) OnDeactivate(ctx context.Context, _ *goakt.GrainProps) error {
	close(owner.stopped)
	var failures []error
	for id, pending := range owner.pending {
		pending.cancel()
		if owner.router != nil {
			if err := goakt.Tell(context.WithoutCancel(ctx), owner.router, goakt.NewBroadcast(llmactor.CancelQuery{
				RequestID: id,
				Cause:     errors.New("llm: query bridge stopped"),
			})); err != nil {
				failures = append(failures, err)
			}
		}
		pending.reply.Response(queryFailure(pending.request, id, errors.New("llm: query bridge stopped")))
	}
	owner.pending = nil
	return errors.Join(failures...)
}

// OnReceive forwards query commands and completes matching deferred requests.
func (owner *Grain) OnReceive(gc *goakt.GrainContext) {
	switch input := gc.Message().(type) {
	case Initialize:
		var err error
		if owner.router != nil || input.Router == nil {
			err = errors.New("llm: query bridge requires one Router binding")
		} else {
			owner.router = input.Router
		}
		gc.Response(llmactor.Completion{
			Err: err,
		})
	case llmactor.CancelQuery:
		if input.RequestID == "" {
			gc.Err(errors.New("llm: query cancellation requires a request identity"))
			return
		}
		if _, exists := owner.pending[input.RequestID]; !exists {
			gc.NoErr()
			return
		}
		failure := goakt.Tell(gc.Context(), owner.router, goakt.NewBroadcast(input))
		if pending, exists := owner.pending[input.RequestID]; exists {
			cause := input.Cause
			if cause == nil {
				cause = context.Canceled
			}
			owner.complete(input.RequestID, queryFailure(pending.request, input.RequestID, errors.Join(cause, failure)))
		}
		if failure != nil {
			gc.Err(failure)
		} else {
			gc.NoErr()
		}
	case llmactor.ListModels:
		owner.start(gc, input.Query, input)
	case llmactor.ResolveModel:
		owner.start(gc, input.Query, input)
	case llmactor.ResolveConfig:
		owner.start(gc, input.Query, input)
	case llmactor.ModelsListed:
		owner.finish(gc, input.RequestID, input)
	case llmactor.ModelResolved:
		owner.finish(gc, input.RequestID, input)
	case llmactor.ConfigResolved:
		owner.finish(gc, input.RequestID, input)
	case queryExpired:
		gc.NoErr()
		if pending, exists := owner.pending[input.id]; exists {
			failure := input.cause
			if err := goakt.Tell(gc.Context(), owner.router, goakt.NewBroadcast(llmactor.CancelQuery{
				RequestID: input.id,
				Cause:     failure,
			})); err != nil {
				failure = errors.Join(failure, err)
			}
			owner.complete(input.id, queryFailure(pending.request, input.id, failure))
		}
	default:
		gc.Unhandled()
	}
}

func (owner *Grain) start(gc *goakt.GrainContext, route llmactor.Query, command any) {
	if owner.router == nil ||
		route.RequestID == "" ||
		route.Deadline.IsZero() ||
		!route.Deadline.After(time.Now()) {
		gc.Response(queryFailure(command, route.RequestID, errors.New("llm: query requires an initialized bridge and future deadline")))
		return
	}
	if _, exists := owner.pending[route.RequestID]; exists {
		gc.Response(queryFailure(command, route.RequestID, errors.New("llm: query identity is already active")))
		return
	}
	reply := gc.DeferResponse()
	if reply == nil {
		gc.Response(queryFailure(command, route.RequestID, errors.New("llm: query bridge requires a request")))
		return
	}
	route.ReplyTo = gc.Self()
	lifetime, cancel := context.WithCancel(context.WithoutCancel(gc.Context()))
	owner.pending[route.RequestID] = queryReply{
		reply:   reply,
		cancel:  cancel,
		request: command,
	}
	switch input := command.(type) {
	case llmactor.ListModels:
		input.Query = route
		command = input
	case llmactor.ResolveModel:
		input.Query = route
		command = input
	case llmactor.ResolveConfig:
		input.Query = route
		command = input

	}
	stopped := owner.stopped
	if err := gc.PipeToSelf(func() (any, error) {
		timer := time.NewTimer(time.Until(route.Deadline))
		defer timer.Stop()
		select {
		case <-timer.C:
			return queryExpired{
				id:    route.RequestID,
				cause: context.DeadlineExceeded,
			}, nil
		case <-lifetime.Done():
			return nil, nil
		case <-stopped:
			return nil, nil
		}
	}); err != nil {
		owner.complete(route.RequestID, queryFailure(command, route.RequestID, err))
		return
	}
	if err := goakt.Tell(gc.Context(), owner.router, goakt.NewBroadcast(command)); err != nil {
		owner.complete(route.RequestID, queryFailure(command, route.RequestID, err))
	}
}

func (owner *Grain) finish(gc *goakt.GrainContext, id llmactor.RequestID, result any) {
	gc.NoErr()
	owner.complete(id, result)
}

func (owner *Grain) complete(id llmactor.RequestID, result any) {
	pending, exists := owner.pending[id]
	if !exists {
		return
	}
	if !queryResponseMatches(pending.request, result) {
		result = queryFailure(pending.request, id, errors.New("llm: query result does not match the requested operation"))
	}
	delete(owner.pending, id)
	pending.cancel()
	pending.reply.Response(result)
}

func queryFailure(command any, id llmactor.RequestID, err error) any {
	switch command.(type) {
	case llmactor.ListModels:
		return llmactor.ModelsListed{
			RequestID: id,
			Err:       err,
		}
	case llmactor.ResolveModel:
		return llmactor.ModelResolved{
			RequestID: id,
			Err:       err,
		}
	case llmactor.ResolveConfig:
		return llmactor.ConfigResolved{
			RequestID: id,
			Err:       err,
		}
	default:
		return llmactor.Completion{
			Err: err,
		}
	}
}

func queryResponseMatches(command any, result any) bool {
	switch command.(type) {
	case llmactor.ListModels:
		_, ok := result.(llmactor.ModelsListed)
		return ok
	case llmactor.ResolveModel:
		_, ok := result.(llmactor.ModelResolved)
		return ok
	case llmactor.ResolveConfig:
		_, ok := result.(llmactor.ConfigResolved)
		return ok
	default:
		return false
	}
}
