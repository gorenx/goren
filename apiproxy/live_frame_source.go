package apiproxy

import (
	"context"
	"errors"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/connection"
	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/session"
	sessionprojection "github.com/gorenx/goren/session/projection"
)

// LiveFrameDependencies are the source-owned state required to produce
// Session and Host downlink frames.
type LiveFrameDependencies struct {
	Sessions    session.LiveStore
	Projections sessionprojection.Registry
}

// LiveFrameOptions contains the injectable frame identity source.
type LiveFrameOptions struct {
	NewRPCID func() (connection.RPCID, error)
}

// LiveFrameSource owns Session lifecycle observers and both connection-level
// downlink feeds. It does not activate Agents or implement unary Session APIs.
type LiveFrameSource struct {
	sessions    session.LiveStore
	projections sessionprojection.Registry
	hub         *liveFrameHub
}

// NewLiveFrameSource installs source-owned observers and returns the downlink
// state object.
func NewLiveFrameSource(
	requestContext context.Context,
	sourceScope *plugin.Scope,
	ports LiveFrameDependencies,
	settings LiveFrameOptions,
) (*LiveFrameSource, error) {
	if requestContext == nil || sourceScope == nil {
		return nil, errors.New("apiproxy: Live Frame Source Context and Scope are required")
	}
	if ports.Sessions == nil || ports.Projections == nil {
		return nil, errors.New("apiproxy: Live Frame Source dependencies are incomplete")
	}
	newRPC := settings.NewRPCID
	if newRPC == nil {
		newRPC = mintFrameRPCID
	}
	owner := &LiveFrameSource{
		sessions: ports.Sessions, projections: ports.Projections,
		hub: newLiveFrameHub(newRPC),
	}
	if err := owner.installObservers(sourceScope); err != nil {
		return nil, err
	}
	if _, err := plugin.Own(sourceScope, "apiProxy.liveFrameSource()", owner.close); err != nil {
		return nil, err
	}
	if err := requestContext.Err(); err != nil {
		return nil, err
	}
	return owner, nil
}

func (owner *LiveFrameSource) installObservers(sourceScope *plugin.Scope) error {
	if _, err := session.OnEvent(sourceScope, owner.observeSessionEvent); err != nil {
		return err
	}
	if _, err := session.OnCreated(sourceScope, owner.observeSessionCreated); err != nil {
		return err
	}
	if _, err := session.OnDisposed(sourceScope, owner.observeSessionDisposed); err != nil {
		return err
	}
	if _, err := agent.OnStatus(sourceScope, owner.observeAgentStatus); err != nil {
		return err
	}
	if _, err := agent.OnError(sourceScope, owner.observeAgentError); err != nil {
		return err
	}
	_, err := owner.projections.OnChanged(
		sourceScope,
		sessionprojection.ChangeListenerFunc(owner.observeProjectionChange),
	)
	return err
}

func (owner *LiveFrameSource) close(context.Context) error {
	owner.hub.close()
	return nil
}

// Mux streams per-session baselines and later committed changes.
func (owner *LiveFrameSource) Mux(
	requestContext context.Context,
	emit func(StreamRequest[MuxFrame]) error,
) error {
	return owner.hub.openMux(requestContext, owner.sessions.List(), emit)
}

// Host streams host-level edges; reconnect baselines remain session.list's responsibility.
func (owner *LiveFrameSource) Host(
	requestContext context.Context,
	emit func(StreamRequest[HostFrame]) error,
) error {
	return owner.hub.openHost(requestContext, emit)
}

// PublishHostFrame lets sibling API domains publish committed Host-level
// changes through the one connection-owned downlink hub.
func (owner *LiveFrameSource) PublishHostFrame(payload HostFrame) error {
	if payload == nil {
		return errors.New("apiproxy: Host frame is nil")
	}
	return owner.hub.hostFrame(payload)
}

// InteractionBroker exposes only the frame capability consumed by the
// interaction gateway.
func (owner *LiveFrameSource) InteractionBroker() InteractionFrameBroker {
	return owner.hub
}

func (owner *LiveFrameSource) observeSessionEvent(
	_ context.Context,
	conversation *session.Session,
	committed session.Event,
) error {
	projected, err := ProjectSessionEvent(committed)
	if err != nil {
		return err
	}
	var queue []QueuedInboxItem
	queueChanged := committed.Type == "agent/inbox/spliced"
	if queueChanged {
		queue, err = projectQueue(conversation.Header(), conversation.Events())
		if err != nil {
			return err
		}
	}
	return owner.hub.sessionEvent(conversation.ID(), projected, queueChanged, queue)
}

func (owner *LiveFrameSource) observeProjectionChange(projectionChange sessionprojection.Change) {
	_ = owner.hub.sessionProjection(
		projectionChange.Session.ID(), projectionChange.Key, projectionChange.Value, projectionChange.Seq,
	)
}

func (owner *LiveFrameSource) observeSessionCreated(
	_ context.Context,
	conversation *session.Session,
) error {
	return owner.hub.sessionCreated(conversation)
}

func (owner *LiveFrameSource) observeSessionDisposed(
	_ context.Context,
	conversation *session.Session,
) error {
	return owner.hub.sessionDisposed(conversation.ID())
}

func (owner *LiveFrameSource) observeAgentStatus(
	_ context.Context,
	subject agent.Agent,
	status agent.Status,
) error {
	return owner.hub.agentStatus(subject.ID(), status == agent.StatusRunning)
}

func (owner *LiveFrameSource) observeAgentError(_ context.Context, notice agent.ErrorNotice) error {
	return owner.hub.agentError(notice.Subject.ID(), notice.Err)
}
