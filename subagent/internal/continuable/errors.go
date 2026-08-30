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

func unauthorizedChild(childSessionID session.SessionID) error {
	return unauthorized(
		fmt.Sprintf("subagent %q belongs to another parent Session", childSessionID),
	)
}

func duplicateChild(childSessionID session.SessionID) error {
	return &subagent.Error{
		Code:    subagent.ErrorDuplicateChild,
		Message: fmt.Sprintf("subagent %q already exists", childSessionID),
	}
}

func notResumable(
	childSessionID session.SessionID,
	reason string,
	cause error,
) error {
	return &subagent.Error{
		Code: subagent.ErrorNotResumable,
		Message: fmt.Sprintf(
			"subagent %q %s",
			childSessionID,
			reason,
		),
		Cause: cause,
	}
}

func descendantAdmissionClosed(childSessionID session.SessionID) error {
	return &subagent.Error{
		Code: subagent.ErrorDraining,
		Message: fmt.Sprintf(
			"subagent %q lost parent descendant admission",
			childSessionID,
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
