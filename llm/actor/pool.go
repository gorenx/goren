package llmactor

import (
	"context"
	"errors"
	"time"

	goakt "github.com/tochemey/goakt/v4/actor"
)

// SpawnPool is a composition-root operation. It initializes every routee before
// returning the Router; the caller must drain calls before stopping the pool.
func SpawnPool(ctx context.Context, system goakt.ActorSystem, name string, size int, routes InitializeWorker) (*goakt.PID, error) {
	if routes.LLM == nil {
		return nil, errors.New("llm: pool requires registry and configuration routes")
	}
	target, err := system.SpawnRouter(ctx, name, size, &Worker{}, goakt.WithConsistentHashRouter(RoutingKey))
	if err != nil {
		return nil, err
	}
	// GoAkt 4.5.3 SpawnRouter does not accept a passivation option. Pause its
	// default idle timer before the initialization mailbox barrier; the pool
	// belongs to the server lifetime, including periods with no model traffic.
	failure := goakt.Tell(ctx, target, &goakt.PausePassivation{})
	if failure == nil {
		failure = initializePool(ctx, system, target, size, routes)
	}
	if failure != nil {
		return nil, errors.Join(failure, target.Shutdown(context.WithoutCancel(ctx)))
	}
	return target, nil
}

func initializePool(ctx context.Context, system goakt.ActorSystem, target *goakt.PID, size int, routes InitializeWorker) error {
	payload, err := goakt.Ask(ctx, target, &goakt.GetRoutees{}, time.Minute)
	if err != nil {
		return err
	}
	members, ok := payload.(*goakt.Routees)
	if !ok || len(members.Names()) != size {
		return errors.New("llm: Router did not initialize the requested pool")
	}
	for _, name := range members.Names() {
		worker, err := system.ActorOf(ctx, name)
		if err != nil {
			return err
		}
		payload, err := goakt.Ask(ctx, worker, routes, time.Minute)
		if err != nil {
			return err
		}
		result, ok := payload.(Completion)
		if !ok {
			return errors.New("llm: invalid worker initialization result")
		}
		if result.Err != nil {
			return result.Err
		}
	}
	return nil
}
