package query

import "fmt"

// ErrorCode is the stable Session Query failure taxonomy used across the
// domain/adapter boundary.
type ErrorCode string

const (
	ErrorAborted         ErrorCode = "SESSION_QUERY_ABORTED"
	ErrorCorruptSession  ErrorCode = "SESSION_QUERY_CORRUPT_SESSION"
	ErrorEventNotFound   ErrorCode = "SESSION_QUERY_EVENT_NOT_FOUND"
	ErrorIndexFailed     ErrorCode = "SESSION_QUERY_INDEX_FAILED"
	ErrorInvalidConfig   ErrorCode = "SESSION_QUERY_INVALID_CONFIG"
	ErrorInvalidCursor   ErrorCode = "SESSION_QUERY_INVALID_CURSOR"
	ErrorInvalidFilter   ErrorCode = "SESSION_QUERY_INVALID_FILTER"
	ErrorInvalidLineage  ErrorCode = "SESSION_QUERY_INVALID_LINEAGE"
	ErrorInvalidLimit    ErrorCode = "SESSION_QUERY_INVALID_LIMIT"
	ErrorInvalidQuery    ErrorCode = "SESSION_QUERY_INVALID_QUERY"
	ErrorInvalidSurface  ErrorCode = "SESSION_QUERY_INVALID_SURFACE"
	ErrorInvalidWindow   ErrorCode = "SESSION_QUERY_INVALID_WINDOW"
	ErrorPersistence     ErrorCode = "SESSION_QUERY_PERSISTENCE_FAILED"
	ErrorSessionNotFound ErrorCode = "SESSION_QUERY_SESSION_NOT_FOUND"
	ErrorSourceConflict  ErrorCode = "SESSION_QUERY_SOURCE_CONFLICT"
	ErrorStaleCursor     ErrorCode = "SESSION_QUERY_STALE_CURSOR"
)

// Error wraps one machine-routable Session Query failure without exposing
// storage implementation details to callers that only need classification.
type Error struct {
	Code  ErrorCode
	Cause error
	Text  string
}

func (problem *Error) Error() string {
	if problem == nil {
		return "session query: unknown failure"
	}
	if problem.Text != "" {
		return problem.Text
	}
	if problem.Cause != nil {
		return "session query: " + problem.Cause.Error()
	}
	return fmt.Sprintf("session query: %s", problem.Code)
}

func (problem *Error) Unwrap() error {
	if problem == nil {
		return nil
	}
	return problem.Cause
}

func failure(code ErrorCode, text string, cause error) error {
	return &Error{Code: code, Text: text, Cause: cause}
}
