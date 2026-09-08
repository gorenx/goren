package workspaceactor

import (
	"context"
	"errors"
	"time"

	"github.com/gorenx/goren/workspace"
	goakt "github.com/tochemey/goakt/v4/actor"
)

// Client adapts non-Actor callers to the mailbox. Actor callers submit Request
// asynchronously and route Receiver results back to their own mailbox.
type Client struct {
	target  *goakt.PID
	timeout time.Duration
}

// NewClient binds a Workspace actor with a bounded admission wait.
func NewClient(target *goakt.PID, timeout time.Duration) (*Client, error) {
	if target == nil || timeout <= 0 {
		return nil, errors.New("workspace: endpoint and positive admission timeout are required")
	}
	return &Client{
		target:  target,
		timeout: timeout,
	}, nil
}

type replies chan Reply

func (target replies) Deliver(result Reply) { target <- result }

func (endpoint *Client) execute(ctx context.Context, command Command) (Reply, error) {
	if ctx == nil {
		return nil, errors.New("workspace: context is required")
	}
	work, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	received := make(replies, 1)
	payload, err := goakt.Ask(context.WithoutCancel(ctx), endpoint.target, Request{
		Context:  work,
		Command:  command,
		Receiver: received,
	}, endpoint.timeout)
	if err != nil {
		cancel(err)
		return nil, err
	}
	admission, ok := payload.(Completion)
	if !ok {
		return nil, errors.New("workspace: invalid admission reply")
	}
	if admission.Err != nil {
		return nil, admission.Err
	}
	result := <-received
	if completion, ok := result.(Completion); ok && completion.Err != nil {
		return nil, completion.Err
	}
	return result, nil
}

// Create registers or resolves a canonical directory.
func (endpoint *Client) Create(ctx context.Context, path string) (workspace.WorkspaceState, bool, error) {
	payload, err := endpoint.execute(ctx, Create{
		Path: path,
	})
	if err != nil {
		return workspace.WorkspaceState{}, false, err
	}
	result, ok := payload.(Created)
	if !ok {
		return workspace.WorkspaceState{}, false, errors.New("workspace: invalid create reply")
	}
	return result.State, result.Created, result.Err
}

// Read resolves a detached workspace view.
func (endpoint *Client) Read(ctx context.Context, id workspace.ID) (View, error) {
	payload, err := endpoint.execute(ctx, Read{
		ID: id,
	})
	if err != nil {
		return View{}, err
	}
	result, ok := payload.(View)
	if !ok {
		return View{}, errors.New("workspace: invalid read reply")
	}
	return result, nil
}

// List returns the complete ordered registration and archive snapshot.
func (endpoint *Client) List(ctx context.Context) (Listing, error) {
	payload, err := endpoint.execute(ctx, List{})
	if err != nil {
		return Listing{}, err
	}
	result, ok := payload.(Listing)
	if !ok {
		return Listing{}, errors.New("workspace: invalid list reply")
	}
	return result, nil
}

// Delete removes one registration.
func (endpoint *Client) Delete(ctx context.Context, id workspace.ID) (bool, error) {
	payload, err := endpoint.execute(ctx, Delete{
		ID: id,
	})
	if err != nil {
		return false, err
	}
	result, ok := payload.(Deleted)
	if !ok {
		return false, errors.New("workspace: invalid delete reply")
	}
	return result.Removed, result.Err
}

// InsertBefore changes registration order.
func (endpoint *Client) InsertBefore(ctx context.Context, input InsertBefore) ([]workspace.ID, error) {
	payload, err := endpoint.execute(ctx, input)
	if err != nil {
		return nil, err
	}
	result, ok := payload.(Order)
	if !ok {
		return nil, errors.New("workspace: invalid order reply")
	}
	return result.IDs, result.Err
}

// Rename updates the workspace title.
func (endpoint *Client) Rename(ctx context.Context, input Rename) error {
	payload, err := endpoint.execute(ctx, input)
	if err != nil {
		return err
	}
	result, ok := payload.(Completion)
	if !ok {
		return errors.New("workspace: invalid mutation reply")
	}
	return result.Err
}

// AttachSession attaches a Session after validating its directory.
func (endpoint *Client) AttachSession(ctx context.Context, input AttachSession) error {
	payload, err := endpoint.execute(ctx, input)
	if err != nil {
		return err
	}
	result, ok := payload.(Completion)
	if !ok {
		return errors.New("workspace: invalid mutation reply")
	}
	return result.Err
}

// DetachSession removes a Session membership.
func (endpoint *Client) DetachSession(ctx context.Context, input DetachSession) error {
	payload, err := endpoint.execute(ctx, input)
	if err != nil {
		return err
	}
	result, ok := payload.(Completion)
	if !ok {
		return errors.New("workspace: invalid mutation reply")
	}
	return result.Err
}

// InsertSessionBefore moves a Session within one workspace.
func (endpoint *Client) InsertSessionBefore(ctx context.Context, input InsertSessionBefore) error {
	payload, err := endpoint.execute(ctx, input)
	if err != nil {
		return err
	}
	result, ok := payload.(Completion)
	if !ok {
		return errors.New("workspace: invalid mutation reply")
	}
	return result.Err
}

// ArchiveSession archives a known Session.
func (endpoint *Client) ArchiveSession(ctx context.Context, input ArchiveSession) error {
	payload, err := endpoint.execute(ctx, input)
	if err != nil {
		return err
	}
	result, ok := payload.(Completion)
	if !ok {
		return errors.New("workspace: invalid mutation reply")
	}
	return result.Err
}
