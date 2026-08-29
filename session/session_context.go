package session

import (
	"context"

	"github.com/gorenx/goren/agentmessage"
)

// sessionContext is the capability object exposed as Context. It composes the
// independent event log and lifecycle owners without giving either a back-reference.
type sessionContext struct {
	// log owns append-only facts and deterministic derived state.
	log *eventLog
	// lifecycle owns operation ordering, membership, and release admission.
	lifecycle sessionLifecycle
}

func newSessionContext(sessionLog *eventLog) *sessionContext {
	return &sessionContext{
		log:       sessionLog,
		lifecycle: newSessionLifecycle(),
	}
}

func (conversation *sessionContext) Header() Header {
	if conversation == nil {
		return Header{}
	}
	return conversation.log.Header()
}

func (conversation *sessionContext) ID() SessionID {
	return conversation.Header().ID
}

func (conversation *sessionContext) FirstLiveSeq() int64 {
	if conversation == nil {
		return 0
	}
	return conversation.log.FirstLiveSeq()
}

func (conversation *sessionContext) Seq() int64 {
	if conversation == nil {
		return 0
	}
	return conversation.log.Seq()
}

func (conversation *sessionContext) Events() []Event {
	if conversation == nil {
		return nil
	}
	return conversation.log.Events()
}

func (conversation *sessionContext) Surface() Surface {
	if conversation == nil {
		return Surface{}
	}
	return conversation.log.Surface()
}

func (conversation *sessionContext) Snapshot() Snapshot {
	if conversation == nil {
		return Snapshot{}
	}
	return conversation.log.snapshot()
}

func (conversation *sessionContext) writeBarrier() WriteBarrier {
	return conversation.log.currentBarrier()
}

func (conversation *sessionContext) DeriveMessages() ([]agentmessage.Message, error) {
	if conversation == nil {
		return nil, nil
	}
	return conversation.log.DeriveMessages()
}

func (conversation *sessionContext) isReentry(requestContext context.Context) bool {
	active, found := requestContext.Value(reentryKey{}).(*sessionContext)
	return found && active == conversation
}

func (conversation *sessionContext) publicationContext(
	requestContext context.Context,
) context.Context {
	return context.WithValue(requestContext, reentryKey{}, conversation)
}
