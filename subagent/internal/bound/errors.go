package bound

import (
	"fmt"

	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/subagent"
)

func boundDisabled(childID session.SessionID) error {
	return &subagent.Error{
		Code: subagent.ErrorBoundDisabled,
		Message: fmt.Sprintf(
			"subagent %q is disabled by its latest Bound config",
			childID,
		),
	}
}

func unauthorizedChild(childID session.SessionID) error {
	return &subagent.Error{
		Code: subagent.ErrorUnauthorized,
		Message: fmt.Sprintf(
			"subagent %q belongs to another parent Session",
			childID,
		),
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

func duplicateChild(childID session.SessionID) error {
	return &subagent.Error{
		Code:    subagent.ErrorDuplicateChild,
		Message: fmt.Sprintf("subagent %q already exists", childID),
	}
}

func bindingNotFound(childID session.SessionID) error {
	return &subagent.Error{
		Code: subagent.ErrorBoundBindingNotFound,
		Message: fmt.Sprintf(
			"subagent %q has no Bound binding in this parent Session",
			childID,
		),
	}
}

func configConflict(
	childID session.SessionID,
	expected int64,
	actual int64,
) error {
	return &subagent.Error{
		Code: subagent.ErrorBoundConfigConflict,
		Message: fmt.Sprintf(
			"subagent %q Bound config revision is %d, expected %d",
			childID,
			actual,
			expected,
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
