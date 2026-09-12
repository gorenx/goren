// Package sessionadapter connects Session API operations to existing Actor owners.
package sessionadapter

import (
	"context"
	"errors"
	"time"

	"github.com/gorenx/goren/agent"
	loopactor "github.com/gorenx/goren/agentloop/actor"
	sessionapi "github.com/gorenx/goren/apiproxy/session"
	"github.com/gorenx/goren/session"
	goakt "github.com/tochemey/goakt/v4/actor"
)

// Agent is the HTTP-side client for one immutable Agent creation.
// It owns no execution state and never creates or closes component actors.
type Agent struct {
	system     goakt.ActorSystem
	endpoint   *goakt.GrainIdentity
	identifier session.SessionID
	role       agent.Role
	parentID   *session.SessionID
	creationID string
	timeout    time.Duration
}

// NewAgent binds an admitted creation without activating any framework identity.
func NewAgent(system goakt.ActorSystem, endpoint *goakt.GrainIdentity, membership agent.Registration, creationID string, timeout time.Duration) (*Agent, error) {
	if system == nil || endpoint == nil || membership.ID == "" || creationID == "" || timeout <= 0 {
		return nil, errors.New("session API: Agent endpoint, identity and positive timeout required")
	}
	var parent *session.SessionID
	if membership.ParentID != nil {
		copied := *membership.ParentID
		parent = &copied
	}
	return &Agent{
		system:     system,
		endpoint:   endpoint,
		identifier: membership.ID,
		role:       membership.Role,
		parentID:   parent,
		creationID: creationID,
		timeout:    timeout,
	}, nil
}

// ID returns the explicit business identity, never a parsed framework name.
func (subject *Agent) ID() session.SessionID { return subject.identifier }

// Role returns the admitted creation's immutable Agent role.
func (subject *Agent) Role() agent.Role { return subject.role }

// ParentID returns current lifetime ownership, independent of historical origin.
func (subject *Agent) ParentID() *session.SessionID {
	if subject.parentID == nil {
		return nil
	}
	copied := *subject.parentID
	return &copied
}

func (subject *Agent) readActivity(ctx context.Context) (loopactor.Activity, error) {
	payload, err := subject.system.AskGrain(ctx, subject.endpoint, loopactor.ReadActivity{
		CreationID: subject.creationID,
	}, subject.timeout)
	if err != nil {
		return loopactor.Activity{}, err
	}
	result, ok := payload.(loopactor.Activity)
	if !ok {
		return loopactor.Activity{}, errors.New("session API: invalid Agent state reply")
	}
	return result, result.Err
}

// Pending reads detached input after checking the original creation identity.
func (subject *Agent) Pending(ctx context.Context) (agent.Pending, error) {
	result, err := subject.readActivity(ctx)
	return result.Pending, err
}

var _ sessionapi.Agent = (*Agent)(nil)

// Status observes the business status of the original creation.
func (subject *Agent) Status(ctx context.Context) (agent.Status, error) {
	result, err := subject.readActivity(ctx)
	return result.Status, err
}
