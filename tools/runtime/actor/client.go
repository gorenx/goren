package runtimeactor

import (
	"context"
	"errors"

	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/passivation"
	"github.com/tochemey/goakt/v4/reentrancy"
)

type releaseStage uint8

const (
	runtimeRetained releaseStage = iota
	runtimeClosed
	runtimeStopped
)

// Client owns Runtime and Watcher lifecycle operations for one Agent creation.
// The caller serializes Start and Close and retains resource responsibility until
// Close succeeds. Registry admission and execution scheduling remain external.
type Client struct {
	system  goakt.ActorSystem
	address *goakt.GrainIdentity
	command InitializeRuntime
	watcher *goakt.PID
	stage   releaseStage
}

// NewClient obtains a framework route and freezes the initialization command without
// acquiring tool implementations. A returned Client must be closed after failure.
func NewClient(ctx context.Context, system goakt.ActorSystem, command InitializeRuntime) (*Client, error) {
	if ctx == nil || system == nil || command.AgentID == "" || command.CreationID == "" {
		return nil, errors.New("tools: Client requires context, ActorSystem and Agent creation")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if command.Config != nil {
		configuration := command.Config.Clone()
		command.Config = &configuration
	}
	address, err := goakt.GrainOf[*Runtime](
		ctx,
		system,
		RuntimeName(string(command.AgentID)),
		goakt.WithLongLivedGrain(),
		goakt.WithGrainReentrancy(reentrancy.New(reentrancy.WithMode(reentrancy.AllowAll))),
	)
	return &Client{
		system:  system,
		address: address,
		command: command,
	}, err
}

// Runtime returns the fixed route used by Registry admission and Agent consumers.
func (sender *Client) Runtime() *goakt.GrainIdentity {
	if sender == nil {
		return nil
	}
	return sender.address
}

// Start initializes tools through an async request from the caller's current turn.
// The caller awaits one result before issuing another lifecycle operation.
func (sender *Client) Start(gc *goakt.GrainContext) <-chan error {
	reply := make(chan error, 1)
	if sender == nil || gc == nil || sender.address == nil || sender.stage != runtimeRetained {
		reply <- errors.New("tools: Client cannot start this runtime")
		return reply
	}
	ctx := context.WithoutCancel(gc.Context())
	sender.request(gc, sender.command, func(failure error) {
		if failure != nil || sender.watcher != nil {
			reply <- failure
			return
		}
		observer, err := sender.system.Spawn(
			ctx,
			string(sender.command.AgentID)+".tools.configuration",
			&Watcher{
				AgentID:    string(sender.command.AgentID),
				CreationID: sender.command.CreationID,
				Target:     sender.address,
			},
			goakt.WithReentrancy(reentrancy.New(reentrancy.WithMode(reentrancy.AllowAll))),
			goakt.WithPassivationStrategy(passivation.NewLongLivedStrategy()),
		)
		sender.watcher = observer
		reply <- err
	})
	return reply
}

// Close stops observation, requests tool release asynchronously, then deactivates
// the original Runtime. An empty instanceID means no activation was authorized.
// The caller freezes admission and awaits one result before retrying.
func (sender *Client) Close(gc *goakt.GrainContext, instanceID string) <-chan error {
	reply := make(chan error, 1)
	if sender == nil || gc == nil || sender.address == nil {
		reply <- errors.New("tools: runtime route is unconfirmed")
		return reply
	}
	if err := gc.Context().Err(); err != nil {
		reply <- err
		return reply
	}
	if sender.stage == runtimeStopped {
		reply <- nil
		return reply
	}
	ctx := context.WithoutCancel(gc.Context())
	if sender.watcher != nil {
		if err := sender.watcher.Shutdown(ctx); err != nil {
			reply <- err
			return reply
		}
		sender.watcher = nil
	}
	if sender.stage == runtimeRetained && instanceID != "" {
		sender.request(gc, CloseRuntime{
			CreationID: sender.command.CreationID,
			InstanceID: instanceID,
		}, func(failure error) {
			if failure != nil {
				reply <- failure
				return
			}
			sender.stage = runtimeClosed
			reply <- sender.stop(ctx)
		})
	} else {
		sender.stage = runtimeClosed
		reply <- sender.stop(ctx)
	}
	return reply
}

func (sender *Client) stop(ctx context.Context) error {
	if err := sender.system.TellGrain(ctx, sender.address, new(goakt.PoisonPill)); err != nil {
		return err
	}
	sender.stage = runtimeStopped
	return nil
}

func (sender *Client) request(gc *goakt.GrainContext, command any, receive func(error)) {
	gc.RequestGrain(sender.address, command,
		goakt.WithRequestTimeout(0),
		goakt.WithReentrancyMode(reentrancy.AllowAll),
	).Then(func(payload any, failure error) {
		if failure == nil {
			result, ok := payload.(Completion)
			if !ok {
				failure = errors.New("tools: invalid Runtime lifecycle response")
			} else {
				failure = result.Err
			}
		}
		receive(failure)
	})
}
