package commandactor

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/gorenx/goren/commands"
	goakt "github.com/tochemey/goakt/v4/actor"
)

// Client binds an ordinary request caller to one existing Agent creation.
// Its blocking methods must not be called inside an Actor mailbox.
type Client struct {
	system     goakt.ActorSystem
	target     *goakt.GrainIdentity
	creationID string
	timeout    time.Duration
}

// NewClient binds an initialized execution owner without activating any Grain.
// The timeout bounds directory and cancellation requests, not command execution.
func NewClient(system goakt.ActorSystem, target *goakt.GrainIdentity, creationID string, timeout time.Duration) (*Client, error) {
	if system == nil ||
		target == nil ||
		creationID == "" ||
		timeout <= 0 {
		return nil, errors.New("commands: client requires an execution route, creation and positive timeout")
	}
	if err := target.Validate(); err != nil {
		return nil, err
	}
	return &Client{
		system:     system,
		target:     target,
		creationID: creationID,
		timeout:    timeout,
	}, nil
}

// List queries the effective directory after validating this Agent creation.
func (source *Client) List(ctx context.Context) ([]commands.Descriptor, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	payload, err := source.system.AskGrain(ctx, source.target, ListCommands{
		CreationID: source.creationID,
	}, source.timeout)
	if err != nil {
		return nil, err
	}
	result, ok := payload.(Listed)
	if !ok {
		return nil, errors.New("commands: invalid directory response")
	}
	return result.Values, result.Err
}

// Execute waits for command settlement using the caller's lifetime.
// Cancellation stops waiting and delivers intent; it does not certify cleanup.
func (source *Client) Execute(ctx context.Context, line string, options commands.ExecuteOptions) (*commands.Execution, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	id := commands.ID(uuid.NewString())
	payload, err := source.system.AskGrain(ctx, source.target, Execute{
		CreationID: source.creationID,
		CommandID:  id,
		Line:       line,
		Options:    options,
	}, time.Duration(1<<63-1))
	if err != nil {
		if ctx.Err() != nil {
			return nil, errors.Join(err, source.cancelCommand(ctx, id))
		}
		return nil, err
	}
	result, ok := payload.(Executed)
	if !ok {
		return nil, errors.New("commands: invalid execution response")
	}
	return result.Execution, result.Err
}

func (source *Client) cancelCommand(ctx context.Context, id commands.ID) error {
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), source.timeout)
	defer cancel()
	payload, err := source.system.AskGrain(cleanup, source.target, Cancel{
		CreationID: source.creationID,
		CommandID:  id,
		Cause:      context.Cause(ctx),
	}, source.timeout)
	if err != nil {
		return err
	}
	result, ok := payload.(Cancelled)
	if !ok {
		return errors.New("commands: invalid cancellation response")
	}
	return result.Err
}
