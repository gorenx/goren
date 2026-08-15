// Package llmretry owns provider-routed request retry recovery and its durable
// Session audit events. Provider packages own RetryPolicy configuration; Agent
// Loop owns request repetition after this package returns a retry decision.
package llmretry

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"

	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/session"
)

const (
	// RetryEventName is the durable record written before one retry wait.
	RetryEventName = "llm/retry"
	// RetryStartedEventName is written after the wait and before the next request.
	RetryStartedEventName = "llm/retry-started"

	maxSafeInteger int64 = 1<<53 - 1
)

// RetryID identifies every scheduled attempt in one provider-policy chain.
type RetryID string

// RetryRecord is the closed durable union for normal and always retry modes.
type RetryRecord interface {
	retryRecord()
}

// NormalRetryRecord is one bounded retry scheduled for a retryable failure.
type NormalRetryRecord struct {
	RetryID    RetryID        `json:"retryId"`
	Turn       int64          `json:"turn"`
	Step       int64          `json:"step"`
	Provider   string         `json:"provider"`
	Mode       llm.RetryMode  `json:"mode"`
	PolicyKey  string         `json:"policyKey"`
	Retry      int64          `json:"retry"`
	MaxRetries int64          `json:"maxRetries"`
	DelayMS    float64        `json:"delayMs"`
	Failure    llm.LlmFailure `json:"failure"`
}

func (NormalRetryRecord) retryRecord() {}

// AlwaysRetryRecord is one unbounded retry scheduled after specialized
// downstream recovery declined or failed.
type AlwaysRetryRecord struct {
	RetryID   RetryID        `json:"retryId"`
	Turn      int64          `json:"turn"`
	Step      int64          `json:"step"`
	Provider  string         `json:"provider"`
	Mode      llm.RetryMode  `json:"mode"`
	PolicyKey string         `json:"policyKey"`
	Retry     int64          `json:"retry"`
	DelayMS   float64        `json:"delayMs"`
	Failure   llm.LlmFailure `json:"failure"`
}

func (AlwaysRetryRecord) retryRecord() {}

// RetryStarted records that one scheduled wait completed and the Agent Loop
// may start the next request attempt inside the same step.
type RetryStarted struct {
	RetryID RetryID `json:"retryId"`
	Turn    int64   `json:"turn"`
	Step    int64   `json:"step"`
	Retry   int64   `json:"retry"`
}

var (
	retryScheduledEvent = session.DefineEvent[RetryRecord](RetryEventName)
	retryStartedEvent   = session.DefineEvent[RetryStarted](RetryStartedEventName)
)

type retryFacts struct {
	chainID    RetryID
	turn       int64
	step       int64
	provider   string
	mode       llm.RetryMode
	policyKey  string
	retry      int64
	maxRetries int64
	delayMS    float64
	failure    llm.LlmFailure
}

func factsFromRecord(entry RetryRecord) (retryFacts, error) {
	switch snapshot := entry.(type) {
	case NormalRetryRecord:
		return retryFacts{
			chainID: snapshot.RetryID, turn: snapshot.Turn, step: snapshot.Step,
			provider: snapshot.Provider, mode: snapshot.Mode, policyKey: snapshot.PolicyKey,
			retry: snapshot.Retry, maxRetries: snapshot.MaxRetries, delayMS: snapshot.DelayMS,
			failure: cloneFailure(snapshot.Failure),
		}, nil
	case AlwaysRetryRecord:
		return retryFacts{
			chainID: snapshot.RetryID, turn: snapshot.Turn, step: snapshot.Step,
			provider: snapshot.Provider, mode: snapshot.Mode, policyKey: snapshot.PolicyKey,
			retry: snapshot.Retry, delayMS: snapshot.DelayMS, failure: cloneFailure(snapshot.Failure),
		}, nil
	default:
		return retryFacts{}, errors.New("llm-retry: unsupported retry record implementation")
	}
}

// DecodeRetryRecord restores the closed durable union and validates its full
// provider-neutral payload. Unknown fields remain forward-compatible.
func DecodeRetryRecord(rawValue json.RawMessage) (RetryRecord, error) {
	var header struct {
		Mode llm.RetryMode `json:"mode"`
	}
	if err := json.Unmarshal(rawValue, &header); err != nil {
		return nil, fmt.Errorf("llm-retry: decode retry record: %w", err)
	}
	var decoded RetryRecord
	switch header.Mode {
	case llm.RetryNormal:
		var bounded NormalRetryRecord
		if err := json.Unmarshal(rawValue, &bounded); err != nil {
			return nil, fmt.Errorf("llm-retry: decode normal retry record: %w", err)
		}
		decoded = bounded
	case llm.RetryAlways:
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(rawValue, &fields); err != nil {
			return nil, fmt.Errorf("llm-retry: decode always retry record: %w", err)
		}
		if _, present := fields["maxRetries"]; present {
			return nil, errors.New("llm-retry: always retry record must omit maxRetries")
		}
		var unbounded AlwaysRetryRecord
		if err := json.Unmarshal(rawValue, &unbounded); err != nil {
			return nil, fmt.Errorf("llm-retry: decode always retry record: %w", err)
		}
		decoded = unbounded
	default:
		return nil, fmt.Errorf("llm-retry: mode must be normal or always, got %q", header.Mode)
	}
	facts, err := factsFromRecord(decoded)
	if err != nil {
		return nil, err
	}
	if err := validateRetryFacts(facts); err != nil {
		return nil, err
	}
	if err := validateFailureWire(rawValue); err != nil {
		return nil, err
	}
	return decoded, nil
}

// DecodeRetryStarted restores and validates one wait-complete transition.
func DecodeRetryStarted(rawValue json.RawMessage) (RetryStarted, error) {
	var transition RetryStarted
	if err := json.Unmarshal(rawValue, &transition); err != nil {
		return RetryStarted{}, fmt.Errorf("llm-retry: decode retry-started record: %w", err)
	}
	if err := validateStartedShape(transition); err != nil {
		return RetryStarted{}, err
	}
	return transition, nil
}

func validateRetryFacts(facts retryFacts) error {
	if facts.chainID == "" {
		return errors.New("llm-retry: retryId must be non-empty")
	}
	if !positiveSafe(facts.turn) || !positiveSafe(facts.step) {
		return errors.New("llm-retry: turn and step must be positive safe integers")
	}
	if facts.provider == "" {
		return errors.New("llm-retry: provider must be non-empty")
	}
	if facts.policyKey == "" {
		return errors.New("llm-retry: policyKey must be non-empty")
	}
	if !positiveSafe(facts.retry) {
		return errors.New("llm-retry: retry must be a positive safe integer")
	}
	switch facts.mode {
	case llm.RetryNormal:
		if !positiveSafe(facts.maxRetries) || facts.retry > facts.maxRetries {
			return fmt.Errorf("llm-retry: retry %d must not exceed positive safe maxRetries %d", facts.retry, facts.maxRetries)
		}
	case llm.RetryAlways:
		if facts.maxRetries != 0 {
			return errors.New("llm-retry: always retry record must omit maxRetries")
		}
	default:
		return fmt.Errorf("llm-retry: mode must be normal or always, got %q", facts.mode)
	}
	if math.IsNaN(facts.delayMS) || math.IsInf(facts.delayMS, 0) ||
		facts.delayMS < 0 || facts.delayMS > llm.MaxTimerDelayMS {
		return fmt.Errorf("llm-retry: delayMs must be within 0..%.0f", llm.MaxTimerDelayMS)
	}
	return validateFailure(facts.failure)
}

func validateStartedShape(transition RetryStarted) error {
	if transition.RetryID == "" {
		return errors.New("llm-retry: retry-started retryId must be non-empty")
	}
	if !positiveSafe(transition.Turn) || !positiveSafe(transition.Step) || !positiveSafe(transition.Retry) {
		return errors.New("llm-retry: retry-started turn, step, and retry must be positive safe integers")
	}
	return nil
}

func validateFailure(problem llm.LlmFailure) error {
	if problem.Message == "" || problem.Code == "" {
		return errors.New("llm-retry: failure message and code must be non-empty")
	}
	if problem.Status != nil && (*problem.Status < 100 || *problem.Status > 599) {
		return errors.New("llm-retry: failure status must be from 100 through 599")
	}
	if problem.ProviderRetryAfterMS != nil &&
		(math.IsNaN(*problem.ProviderRetryAfterMS) || math.IsInf(*problem.ProviderRetryAfterMS, 0) || *problem.ProviderRetryAfterMS <= 0) {
		return errors.New("llm-retry: failure providerRetryAfterMs must be positive and finite")
	}
	return nil
}

func validateFailureWire(rawValue json.RawMessage) error {
	var recordFields map[string]json.RawMessage
	if err := json.Unmarshal(rawValue, &recordFields); err != nil {
		return err
	}
	failureRaw, present := recordFields["failure"]
	if !present {
		return errors.New("llm-retry: failure must be present")
	}
	var failureFields map[string]json.RawMessage
	if err := json.Unmarshal(failureRaw, &failureFields); err != nil || failureFields == nil {
		return errors.New("llm-retry: failure must be an object")
	}
	if requestRaw, exists := failureFields["requestId"]; exists {
		var requestIdentifier string
		if err := json.Unmarshal(requestRaw, &requestIdentifier); err != nil || requestIdentifier == "" {
			return errors.New("llm-retry: failure requestId must be a non-empty string when present")
		}
	}
	return nil
}

func positiveSafe(value int64) bool {
	return value > 0 && value <= maxSafeInteger
}

func cloneFailure(source llm.LlmFailure) llm.LlmFailure {
	detached := source
	if source.Status != nil {
		statusCode := *source.Status
		detached.Status = &statusCode
	}
	if source.ProviderRetryAfterMS != nil {
		retryAfter := *source.ProviderRetryAfterMS
		detached.ProviderRetryAfterMS = &retryAfter
	}
	return detached
}
