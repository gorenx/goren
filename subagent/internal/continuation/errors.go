package continuation

import (
	"context"
	"errors"
	"fmt"

	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/subagent"
)

func checkContext(requestContext context.Context, operation string) error {
	if requestContext == nil {
		return errors.New("subagent: " + operation + " context is nil")
	}
	return requestContext.Err()
}

func unauthorized(message string) error {
	return &subagent.Error{
		Code:    subagent.ErrorUnauthorized,
		Message: message,
	}
}

func duplicateChild(childID session.SessionID) error {
	return &subagent.Error{
		Code:    subagent.ErrorDuplicateChild,
		Message: fmt.Sprintf("subagent %q already exists", childID),
	}
}
