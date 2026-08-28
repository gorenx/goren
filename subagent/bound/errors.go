package bound

// ErrorCode is the stable Bound Definition management failure vocabulary.
type ErrorCode string

const (
	ErrorDefinitionExists   ErrorCode = "BOUND_DEFINITION_EXISTS"
	ErrorDefinitionNotFound ErrorCode = "BOUND_DEFINITION_NOT_FOUND"
	ErrorDefinitionConflict ErrorCode = "BOUND_DEFINITION_CONFLICT"
	ErrorDefinitionRejected ErrorCode = "BOUND_DEFINITION_REJECTED"
)

// Error is one typed Definition failure with an optional technical cause.
type Error struct {
	Code    ErrorCode
	Message string
	Cause   error
}

func (problem *Error) Error() string {
	if problem == nil {
		return "subagent/bound: <nil error>"
	}
	return problem.Message
}

func (problem *Error) Unwrap() error {
	if problem == nil {
		return nil
	}
	return problem.Cause
}
