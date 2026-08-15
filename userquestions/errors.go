package userquestions

const (
	CodeNoProvider      = "NO_PROVIDER"
	CodeDuplicate       = "DUPLICATE_PROVIDER"
	CodeAborted         = "ASK_ABORTED"
	CodeCancelled       = "ASK_CANCELLED"
	CodeEmptyQuestions  = "EMPTY_QUESTIONS"
	CodeCallerNotLive   = "CALLER_NOT_LIVE"
	CodeDelegatedCaller = "DELEGATED_CALLER"
	CodeBadIntent       = "BAD_INTENT"
	CodeMissingAgent    = "ASK_MISSING_AGENT"
)

// Error carries the stable UserQuestionError class and routing code.
type Error struct {
	Message string
	Code    string
	Cause   error
}

func (problem *Error) Error() string {
	if problem == nil {
		return "userquestions: failure"
	}
	return problem.Message
}

func (problem *Error) Unwrap() error {
	if problem == nil {
		return nil
	}
	return problem.Cause
}

// ToolErrorName exposes the pinned error class to Tools without a package dependency.
func (*Error) ToolErrorName() string { return "UserQuestionError" }

// ToolErrorCode exposes the stable failure code to Tools.
func (problem *Error) ToolErrorCode() string {
	if problem == nil {
		return "UNKNOWN"
	}
	return problem.Code
}

func newError(message string, code string, cause ...error) *Error {
	problem := &Error{Message: message, Code: code}
	if len(cause) != 0 {
		problem.Cause = cause[0]
	}
	return problem
}
