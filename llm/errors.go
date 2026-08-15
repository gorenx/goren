package llm

import (
	"errors"
	"fmt"
	"math"
)

const (
	// ContextWindowExceededCode identifies a request larger than the selected model context.
	ContextWindowExceededCode = "CONTEXT_WINDOW_EXCEEDED"
	// QuotaExceededCode identifies exhausted provider balance or quota.
	QuotaExceededCode = "QUOTA"
	// EmptyResponseCode identifies a successful provider response with no content.
	EmptyResponseCode = "EMPTY_RESPONSE"
	// InvalidCredentialCode identifies a supplied credential that cannot be sent safely.
	InvalidCredentialCode = "INVALID_CREDENTIAL"
)

// LlmFailure is the serializable provider-neutral failure carried by terminal
// stream chunks and Agent request-recovery policy.
type LlmFailure struct {
	Message              string            `json:"message"`
	Code                 string            `json:"code"`
	Status               *int              `json:"status,omitempty"`
	ProviderRetryAfterMS *float64          `json:"providerRetryAfterMs,omitempty"`
	RequestID            ProviderRequestID `json:"requestId,omitempty"`
}

// LlmErrorOptions contains optional provider facts and an error cause.
type LlmErrorOptions struct {
	Status               *int
	ProviderRetryAfterMS *float64
	RequestID            ProviderRequestID
	Cause                error
}

// LlmError carries a stable machine-routing code and detached provider facts.
type LlmError struct {
	failure LlmFailure
	cause   error
}

type failureCarrier interface {
	Failure() LlmFailure
}

// NewLlmError validates and constructs one Harness LLM failure.
func NewLlmError(summary string, failureCode string, options LlmErrorOptions) (*LlmError, error) {
	if summary == "" {
		return nil, errors.New("llm: error message must be non-empty")
	}
	if failureCode == "" {
		return nil, errors.New("llm: error code must be non-empty")
	}
	if options.Status != nil && (*options.Status < 100 || *options.Status > 599) {
		return nil, errors.New("llm: error status must be from 100 through 599")
	}
	if options.ProviderRetryAfterMS != nil &&
		(math.IsNaN(*options.ProviderRetryAfterMS) || math.IsInf(*options.ProviderRetryAfterMS, 0) || *options.ProviderRetryAfterMS <= 0) {
		return nil, errors.New("llm: providerRetryAfterMs must be positive and finite")
	}
	if options.RequestID == "" {
		options.RequestID = ProviderRequestID("")
	}
	return &LlmError{
		failure: LlmFailure{
			Message: summary, Code: failureCode, Status: cloneInt(options.Status),
			ProviderRetryAfterMS: cloneFloat(options.ProviderRetryAfterMS), RequestID: options.RequestID,
		},
		cause: options.Cause,
	}, nil
}

// MustLlmError constructs an LlmError for package-owned constant inputs.
func MustLlmError(summary string, failureCode string, options ...LlmErrorOptions) *LlmError {
	settings := LlmErrorOptions{}
	if len(options) > 0 {
		settings = options[0]
	}
	problem, err := NewLlmError(summary, failureCode, settings)
	if err != nil {
		panic(err)
	}
	return problem
}

func (problem *LlmError) Error() string {
	if problem == nil {
		return "llm: failure"
	}
	return problem.failure.Message
}

// Unwrap returns the adapter or transport cause.
func (problem *LlmError) Unwrap() error {
	if problem == nil {
		return nil
	}
	return problem.cause
}

// Code returns the stable machine-routing code.
func (problem *LlmError) Code() string {
	if problem == nil {
		return "UNKNOWN"
	}
	return problem.failure.Code
}

// Failure returns a detached serializable failure snapshot.
func (problem *LlmError) Failure() LlmFailure {
	if problem == nil {
		return LlmFailure{Message: "LLM adapter failed", Code: "UNKNOWN"}
	}
	return cloneFailure(problem.failure)
}

func normalizeLlmFailure(value error) LlmFailure {
	if value == nil {
		return LlmFailure{Message: "LLM adapter failed", Code: "UNKNOWN"}
	}
	var carrier failureCarrier
	if errors.As(value, &carrier) {
		candidate := carrier.Failure()
		if validateFailure(candidate) == nil {
			return cloneFailure(candidate)
		}
	}
	return LlmFailure{Message: value.Error(), Code: "UNKNOWN"}
}

func validateFailure(candidate LlmFailure) error {
	if candidate.Message == "" || candidate.Code == "" {
		return errors.New("llm: failure message and code must be non-empty")
	}
	if candidate.Status != nil && (*candidate.Status < 100 || *candidate.Status > 599) {
		return errors.New("llm: failure status is invalid")
	}
	if candidate.ProviderRetryAfterMS != nil &&
		(math.IsNaN(*candidate.ProviderRetryAfterMS) || math.IsInf(*candidate.ProviderRetryAfterMS, 0) || *candidate.ProviderRetryAfterMS <= 0) {
		return errors.New("llm: failure retry delay is invalid")
	}
	return nil
}

func cloneFailure(source LlmFailure) LlmFailure {
	detached := source
	detached.Status = cloneInt(source.Status)
	detached.ProviderRetryAfterMS = cloneFloat(source.ProviderRetryAfterMS)
	return detached
}

func cloneInt(source *int) *int {
	if source == nil {
		return nil
	}
	value := *source
	return &value
}

func cloneFloat(source *float64) *float64 {
	if source == nil {
		return nil
	}
	value := *source
	return &value
}

func failureError(prefix string, value error) error {
	if value == nil {
		return errors.New(prefix)
	}
	return fmt.Errorf("%s: %w", prefix, value)
}
