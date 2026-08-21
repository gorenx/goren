package sessionapi

import (
	"fmt"

	"github.com/gorenx/goren/session"
)

// CWDConflictError reports an attempt to adopt a Session under a different
// working directory.
type CWDConflictError struct {
	identifier session.SessionID
	requested  string
	existing   *string
}

func (problem *CWDConflictError) Error() string {
	return fmt.Sprintf(
		"session %q already exists with cwd %s; requested %q",
		problem.identifier, quotedOptional(problem.existing), problem.requested,
	)
}

func (problem *CWDConflictError) SessionID() session.SessionID { return problem.identifier }
func (problem *CWDConflictError) RequestedCWD() string         { return problem.requested }
func (problem *CWDConflictError) ExistingCWD() *string         { return cloneStringPointer(problem.existing) }

// PresetConflictError reports an attempt to adopt a Session under a different
// Agent preset.
type PresetConflictError struct {
	identifier session.SessionID
	requested  string
	existing   *string
}

func (problem *PresetConflictError) Error() string {
	if problem.existing == nil {
		return fmt.Sprintf(
			"session %q records no agent preset, so it cannot be adopted under %q",
			problem.identifier, problem.requested,
		)
	}
	return fmt.Sprintf(
		"session %q already runs agent preset %q; requested %q",
		problem.identifier, *problem.existing, problem.requested,
	)
}

func (problem *PresetConflictError) SessionID() session.SessionID { return problem.identifier }
func (problem *PresetConflictError) RequestedPreset() string      { return problem.requested }
func (problem *PresetConflictError) ExistingPreset() *string {
	return cloneStringPointer(problem.existing)
}

// SubagentOwnershipError prevents ordinary Session APIs from taking over a
// child Session owned by subagent routing.
type SubagentOwnershipError struct {
	identifier session.SessionID
}

func (problem *SubagentOwnershipError) Error() string {
	return fmt.Sprintf("session %q is owned by subagent routing", problem.identifier)
}

func (problem *SubagentOwnershipError) SessionID() session.SessionID { return problem.identifier }

func quotedOptional(textValue *string) string {
	if textValue == nil {
		return "undefined"
	}
	return fmt.Sprintf("%q", *textValue)
}
