// Package agentclient is the Subagent-owned GoAkt client for exact Agent
// creations. It stores framework routes, never Agent business owners.
package agentclient

import (
	"context"
	"errors"
	"time"

	"github.com/gorenx/goren/agent"
	agentactor "github.com/gorenx/goren/agent/actor"
	agentregistry "github.com/gorenx/goren/agent/registry"
	loopactor "github.com/gorenx/goren/agentloop/actor"
	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/session"
	sessionactor "github.com/gorenx/goren/session/actor"
	goakt "github.com/tochemey/goakt/v4/actor"
)

// Agent is the narrow Subagent view of one exact Agent creation.
type Agent interface {
	Creation() agentregistry.Creation
	ID() session.SessionID
	Read(context.Context) (sessionactor.View, error)
	Commit(context.Context, session.WritePlan) (session.WriteResult, error)
	Flush(context.Context, session.WriteBarrier) error
	Activity(context.Context) (loopactor.Activity, error)
	Followup(context.Context, agentmessage.UserMessage) error
	Steer(context.Context, agentmessage.UserMessage) error
	Inject(context.Context, agentmessage.UserMessage) error
	Cancel(context.Context, agent.CancelCause, agent.CancelOptions) error
	WhenIdle(context.Context) error
	Close(context.Context) error
}

func (endpoint *Client) Commit(ctx context.Context, plan session.WritePlan) (session.WriteResult, error) {
	payload, err := endpoint.system.AskGrain(ctx, endpoint.session, sessionactor.Commit{
		CreationID: endpoint.creation.CreationID,
		Plan:       plan,
	}, endpoint.timeout)
	if err != nil {
		return session.WriteResult{}, err
	}
	committed, ok := payload.(sessionactor.Committed)
	if !ok {
		return session.WriteResult{}, errors.New("subagent: Session returned an invalid commit response")
	}
	return committed.Result, committed.Err
}

func (endpoint *Client) Flush(ctx context.Context, barrier session.WriteBarrier) error {
	payload, err := endpoint.system.AskGrain(ctx, endpoint.session, sessionactor.Flush{
		CreationID: endpoint.creation.CreationID,
		Barrier:    barrier,
	}, endpoint.timeout)
	if err != nil {
		return err
	}
	flushed, ok := payload.(sessionactor.Flushed)
	if !ok {
		return errors.New("subagent: Session returned an invalid flush response")
	}
	return flushed.Err
}

// Client addresses one exact ready Agent creation.
type Client struct {
	system    goakt.ActorSystem
	authority *goakt.GrainIdentity
	creation  agentregistry.Creation
	executor  *goakt.GrainIdentity
	session   *goakt.GrainIdentity
	timeout   time.Duration
}

// Directory resolves and creates exact Agent lifetimes through one Registry.
type Directory struct {
	system    goakt.ActorSystem
	authority *goakt.GrainIdentity
	timeout   time.Duration
}

// NewDirectory binds existing GoAkt infrastructure without starting an Actor.
func NewDirectory(system goakt.ActorSystem, authority *goakt.GrainIdentity, timeout time.Duration) (*Directory, error) {
	if system == nil || authority == nil || timeout <= 0 {
		return nil, errors.New("subagent: Agent client requires system, Registry, and timeout")
	}
	return &Directory{
		system:    system,
		authority: authority,
		timeout:   timeout,
	}, nil
}

// Find resolves the current ready creation for one Agent ID.
func (directory *Directory) Find(ctx context.Context, identifier session.SessionID) (Agent, error) {
	payload, err := directory.system.AskGrain(ctx, directory.authority, agentregistry.FindAgent{
		AgentID: identifier,
	}, directory.timeout)
	if err != nil {
		return nil, err
	}
	ready, ok := payload.(agentregistry.Ready)
	if !ok {
		return nil, errors.New("subagent: Agent Registry returned an invalid response")
	}
	if ready.Err != nil {
		return nil, ready.Err
	}
	return directory.client(ctx, ready)
}

// CreateChild asks Agent Registry to derive the child's frozen composition.
func (directory *Directory) CreateChild(ctx context.Context, input agentregistry.CreateChildAgent) (Agent, error) {
	payload, err := directory.system.AskGrain(ctx, directory.authority, input, directory.timeout)
	if err != nil {
		return nil, err
	}
	ready, ok := payload.(agentregistry.Ready)
	if !ok {
		return nil, errors.New("subagent: Agent Registry returned an invalid child response")
	}
	if ready.Err != nil {
		return nil, ready.Err
	}
	return directory.client(ctx, ready)
}

func (directory *Directory) client(ctx context.Context, ready agentregistry.Ready) (*Client, error) {
	conversation, err := goakt.GrainOf[*sessionactor.Grain](
		ctx,
		directory.system,
		sessionactor.RuntimeName(string(ready.Creation.AgentID)),
		goakt.WithLongLivedGrain(),
	)
	if err != nil {
		return nil, err
	}
	return &Client{
		system:    directory.system,
		authority: directory.authority,
		creation:  ready.Creation,
		executor:  ready.Executor,
		session:   conversation,
		timeout:   directory.timeout,
	}, nil
}

func (endpoint *Client) Creation() agentregistry.Creation { return endpoint.creation }
func (endpoint *Client) ID() session.SessionID            { return endpoint.creation.AgentID }

// Read returns one detached committed Session view.
func (endpoint *Client) Read(ctx context.Context) (sessionactor.View, error) {
	payload, err := endpoint.system.AskGrain(ctx, endpoint.session, sessionactor.Read{
		CreationID: endpoint.creation.CreationID,
	}, endpoint.timeout)
	if err != nil {
		return sessionactor.View{}, err
	}
	view, ok := payload.(sessionactor.View)
	if !ok {
		return sessionactor.View{}, errors.New("subagent: Session returned an invalid view")
	}
	return view, view.Err
}

func (endpoint *Client) Activity(ctx context.Context) (loopactor.Activity, error) {
	payload, err := endpoint.system.AskGrain(ctx, endpoint.executor, loopactor.ReadActivity{
		CreationID: endpoint.creation.CreationID,
	}, endpoint.timeout)
	if err != nil {
		return loopactor.Activity{}, err
	}
	activity, ok := payload.(loopactor.Activity)
	if !ok {
		return loopactor.Activity{}, errors.New("subagent: AgentLoop returned an invalid activity response")
	}
	return activity, activity.Err
}

func (endpoint *Client) Followup(ctx context.Context, message agentmessage.UserMessage) error {
	return endpoint.changed(ctx, loopactor.Followup{
		CreationID: endpoint.creation.CreationID,
		Message:    message,
	})
}

func (endpoint *Client) Steer(ctx context.Context, message agentmessage.UserMessage) error {
	return endpoint.changed(ctx, loopactor.Steer{
		CreationID: endpoint.creation.CreationID,
		Message:    message,
	})
}

func (endpoint *Client) Inject(ctx context.Context, message agentmessage.UserMessage) error {
	return endpoint.changed(ctx, loopactor.Inject{
		CreationID: endpoint.creation.CreationID,
		Message:    message,
	})
}

func (endpoint *Client) Cancel(ctx context.Context, cause agent.CancelCause, options agent.CancelOptions) error {
	return endpoint.changed(ctx, loopactor.Cancel{
		CreationID: endpoint.creation.CreationID,
		Cause:      cause,
		Options:    options,
	})
}

func (endpoint *Client) changed(ctx context.Context, input any) error {
	payload, err := endpoint.system.AskGrain(ctx, endpoint.executor, input, endpoint.timeout)
	if err != nil {
		return err
	}
	changed, ok := payload.(loopactor.Changed)
	if !ok {
		return errors.New("subagent: AgentLoop returned an invalid response")
	}
	return changed.Err
}

func (endpoint *Client) WhenIdle(ctx context.Context) error {
	payload, err := endpoint.system.AskGrain(ctx, endpoint.executor, loopactor.WhenIdle{
		CreationID: endpoint.creation.CreationID,
	}, endpoint.timeout)
	if err != nil {
		return err
	}
	completed, ok := payload.(loopactor.Completion)
	if !ok {
		return errors.New("subagent: AgentLoop returned an invalid idle response")
	}
	return completed.Err
}

func (endpoint *Client) Close(ctx context.Context) error {
	payload, err := endpoint.system.AskGrain(ctx, endpoint.authority, agentregistry.CloseAgent{
		Creation: endpoint.creation,
	}, endpoint.timeout)
	if err != nil {
		return err
	}
	closed, ok := payload.(agentactor.Closed)
	if !ok {
		return errors.New("subagent: Agent Registry returned an invalid close response")
	}
	return closed.Err
}
