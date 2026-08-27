package continuable

import (
	"fmt"

	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/subagent"
)

func unauthorized(message string) error {
	return &subagent.Error{
		Code:    subagent.ErrorUnauthorized,
		Message: message,
	}
}

func unauthorizedChild(childID session.SessionID) error {
	return unauthorized(
		fmt.Sprintf("subagent %q belongs to another parent Session", childID),
	)
}

func duplicateChild(childID session.SessionID) error {
	return &subagent.Error{
		Code:    subagent.ErrorDuplicateChild,
		Message: fmt.Sprintf("subagent %q already exists", childID),
	}
}

func notResumable(
	childID session.SessionID,
	reason string,
	cause error,
) error {
	return &subagent.Error{
		Code: subagent.ErrorNotResumable,
		Message: fmt.Sprintf(
			"subagent %q %s",
			childID,
			reason,
		),
		Cause: cause,
	}
}

func descendantAdmissionClosed(childID session.SessionID) error {
	return &subagent.Error{
		Code: subagent.ErrorDraining,
		Message: fmt.Sprintf(
			"subagent %q lost parent descendant admission",
			childID,
		),
	}
}

func noSeedBuilder(builderName string) error {
	return &subagent.Error{
		Code: subagent.ErrorNoSeedBuilder,
		Message: fmt.Sprintf(
			"no subagent SeedBuilder registered for %q",
			builderName,
		),
	}
}
