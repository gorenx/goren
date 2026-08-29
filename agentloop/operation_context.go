package agentloop

import (
	"context"
	"errors"
)

// contextFailure returns the stable cause of one Agent operation Context. It
// is shared by construction and RLA orchestration so both boundaries preserve
// typed cancellation causes.
func contextFailure(requestContext context.Context) error {
	if requestContext == nil {
		return errors.New("agentloop: operation Context is nil")
	}
	if requestContext.Err() == nil {
		return nil
	}
	if cause := context.Cause(requestContext); cause != nil {
		return cause
	}
	return requestContext.Err()
}
