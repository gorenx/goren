package registry

import (
	"context"
	"errors"
	"fmt"

	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/reentrancy"
)

// initializer confirms one capability, including its owner-specific protocol.
type initializer interface {
	Initialize(*goakt.GrainContext, chan<- componentResult)
	Runtime() *goakt.GrainIdentity
}

// releaser confirms business release and any required framework deactivation.
type releaser interface {
	Close(*goakt.GrainContext, chan<- error)
}

type componentResult struct {
	err error
	// unowned is true only when the owner explicitly rejected acquisition.
	// Missing or invalid replies retain the original cleanup responsibility.
	unowned bool
}

// runtimeRelease retains only the boundary between an acknowledged business
// close and framework deactivation. A failed stop must not repeat business close.
type runtimeRelease struct {
	identity  *goakt.GrainIdentity
	confirmed bool
}

// clientMessage resumes a protocol with the current receive context. GrainContext
// is pooled by GoAkt and must not be retained for a later request or activation.
type clientMessage interface {
	deliver(*goakt.GrainContext)
}

type clientReply[T any] struct {
	value   T
	err     error
	receive func(*goakt.GrainContext, T, error)
}

func (result clientReply[T]) deliver(gc *goakt.GrainContext) {
	result.receive(gc, result.value, result.err)
}

func requestComponent[T any](
	gc *goakt.GrainContext,
	target *goakt.GrainIdentity,
	message any,
	receive func(*goakt.GrainContext, T, error),
) {
	completed := make(chan clientReply[T], 1)
	err := gc.PipeToSelf(func() (any, error) { return <-completed, nil })
	if err != nil {
		var empty T
		receive(gc, empty, err)
		return
	}
	gc.RequestGrain(
		target, message, goakt.WithRequestTimeout(0),
		goakt.WithReentrancyMode(reentrancy.AllowAll),
	).Then(
		func(payload any, failure error) {
			response, valid := payload.(T)
			if failure == nil && !valid {
				failure = fmt.Errorf("agent registry: invalid component reply %T", payload)
			}
			completed <- clientReply[T]{
				value:   response,
				err:     failure,
				receive: receive,
			}
		},
	)
}

func closeRuntime[T any](
	gc *goakt.GrainContext,
	endpoint *runtimeRelease,
	message any,
	failureOf func(T) error,
	reply chan<- error,
) {
	if endpoint.identity == nil {
		reply <- errors.New("agent registry: resource endpoint is unconfirmed")
		return
	}
	if endpoint.confirmed {
		deactivate(gc, endpoint.identity, reply)
		return
	}
	requestComponent(gc, endpoint.identity, message, func(gc *goakt.GrainContext, response T, failure error) {
		if failure == nil {
			failure = failureOf(response)
		}
		if failure != nil {
			reply <- failure
			return
		}
		endpoint.confirmed = true
		deactivate(gc, endpoint.identity, reply)
	})
}

func deactivate(gc *goakt.GrainContext, identity *goakt.GrainIdentity, reply chan<- error) {
	if identity == nil {
		reply <- errors.New("agent registry: activation endpoint is unconfirmed")
		return
	}
	ctx := context.WithoutCancel(gc.Context())
	system := gc.ActorSystem()
	err := gc.PipeToSelf(func() (any, error) {
		reply <- system.TellGrain(ctx, identity, new(goakt.PoisonPill))
		return nil, nil
	})
	if err != nil {
		reply <- err
	}
}
