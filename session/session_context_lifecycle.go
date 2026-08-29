package session

import "context"

// The methods in this file are the Session aggregate boundary used by
// memoryStore. They prevent Store code from reaching into eventLog or
// sessionLifecycle state and keep lifecycle decisions with the Session owner.

func (conversation *sessionContext) beginEnter() error {
	return conversation.lifecycle.beginEnter()
}

func (conversation *sessionContext) finishEnter(
	token *membershipToken,
	publisher eventPublisher,
	succeeded bool,
) error {
	return conversation.lifecycle.finishEnter(token, publisher, succeeded)
}

func (conversation *sessionContext) beginAnnounce(
	token *membershipToken,
) (eventPublisher, error) {
	return conversation.lifecycle.beginAnnounce(token)
}

func (conversation *sessionContext) finishAnnounce(
	token *membershipToken,
) error {
	return conversation.lifecycle.finishAnnounce(token)
}

func (conversation *sessionContext) beginFlush(
	token *membershipToken,
) (eventPublisher, error) {
	return conversation.lifecycle.beginFlush(token)
}

func (conversation *sessionContext) finishFlush() {
	conversation.lifecycle.finishFlush()
}

func (conversation *sessionContext) requestRelease(
	token *membershipToken,
	ownedContext context.Context,
) (*releaseAttempt, error) {
	return conversation.lifecycle.requestRelease(token, ownedContext)
}

func (conversation *sessionContext) startRelease(
	token *membershipToken,
	attempt *releaseAttempt,
) (releaseDecision, error) {
	return conversation.lifecycle.startRelease(token, attempt)
}

func (conversation *sessionContext) completeRelease(
	attempt *releaseAttempt,
	succeeded bool,
	releaseErr error,
) error {
	return conversation.lifecycle.completeRelease(attempt, succeeded, releaseErr)
}

func (conversation *sessionContext) visible() bool {
	return conversation.lifecycle.visible()
}

func (conversation *sessionContext) terminalAdmission() bool {
	return conversation.lifecycle.terminalAdmission()
}

func (conversation *sessionContext) pendingOperations() int {
	return conversation.lifecycle.pendingOperations()
}

func (conversation *sessionContext) currentPhase() sessionPhase {
	return conversation.lifecycle.currentPhase()
}
