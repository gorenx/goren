package tools

import (
	"fmt"
	"strings"
)

// CodedError exposes stable tool-routing metadata without parsing messages.
type CodedError interface {
	error
	ToolErrorName() string
	ToolErrorCode() string
}

// ToolNotFoundError reports an absent or invisible tool.
type ToolNotFoundError struct {
	Name          string
	ReachableFrom string
}

func (failure *ToolNotFoundError) Error() string {
	if failure.ReachableFrom == "" {
		return fmt.Sprintf("unknown tool %q", failure.Name)
	}
	return fmt.Sprintf("unknown tool %q: %s", failure.Name, failure.ReachableFrom)
}

// ToolErrorName returns the stable error class.
func (*ToolNotFoundError) ToolErrorName() string { return "ToolNotFoundError" }

// ToolErrorCode returns the stable routing code.
func (*ToolNotFoundError) ToolErrorCode() string { return "UNKNOWN_TOOL" }

// ToolArgumentsError reports input-schema violations.
type ToolArgumentsError struct {
	Violations []string
}

func (failure *ToolArgumentsError) Error() string {
	return "invalid arguments: " + strings.Join(failure.Violations, "; ")
}

// ToolErrorName returns the stable error class.
func (*ToolArgumentsError) ToolErrorName() string { return "ToolArgsError" }

// ToolErrorCode returns the stable routing code.
func (*ToolArgumentsError) ToolErrorCode() string { return "INVALID_ARGS" }

// ToolOutputError reports output-schema or projection violations.
type ToolOutputError struct {
	Name       string
	Violations []string
}

func (failure *ToolOutputError) Error() string {
	return fmt.Sprintf("tool %q returned invalid output: %s", failure.Name, strings.Join(failure.Violations, "; "))
}

// ToolErrorName returns the stable error class.
func (*ToolOutputError) ToolErrorName() string { return "ToolOutputError" }

// ToolErrorCode returns the stable routing code.
func (*ToolOutputError) ToolErrorCode() string { return "INVALID_TOOL_OUTPUT" }

type abortError struct {
	message string
	code    string
}

func (failure *abortError) Error() string         { return failure.message }
func (*abortError) ToolErrorName() string         { return "AbortError" }
func (failure *abortError) ToolErrorCode() string { return failure.code }
