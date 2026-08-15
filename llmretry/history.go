package llmretry

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/gorenx/goren/session"
)

type retryChainKey struct {
	turn      int64
	step      int64
	provider  string
	policyKey string
}

type retryChainState struct {
	chainID RetryID
	retry   int64
	latest  RetryRecord
}

type scheduledAttemptKey struct {
	chainID RetryID
	retry   int64
}

type scheduledAttempt struct {
	turn int64
	step int64
}

type historyProjection struct {
	turnOpen    bool
	turn        int64
	stepOpen    bool
	stepTurn    int64
	step        int64
	provider    string
	chains      map[retryChainKey]retryChainState
	chainOwners map[RetryID]retryChainKey
	scheduled   map[scheduledAttemptKey]scheduledAttempt
	started     map[scheduledAttemptKey]struct{}
}

func newHistoryProjection() *historyProjection {
	return &historyProjection{
		chains: make(map[retryChainKey]retryChainState), chainOwners: make(map[RetryID]retryChainKey),
		scheduled: make(map[scheduledAttemptKey]scheduledAttempt), started: make(map[scheduledAttemptKey]struct{}),
	}
}

// ValidateHistory validates every durable retry transition in event order.
// Core Session events are read only to establish the open turn, step, and
// provider route that the retry records name.
func ValidateHistory(events []session.Event) error {
	_, err := analyzeHistory(events)
	return err
}

func analyzeHistory(events []session.Event) (*historyProjection, error) {
	projection := newHistoryProjection()
	for _, committed := range events {
		if err := projection.advance(committed); err != nil {
			return nil, fmt.Errorf("llm-retry: invalid Session event at seq %d: %w", committed.Seq, err)
		}
	}
	return projection, nil
}

func (projection *historyProjection) advance(committed session.Event) error {
	switch committed.Type {
	case session.TurnStartEventName:
		var boundary session.TurnStart
		if err := json.Unmarshal(committed.Data, &boundary); err != nil {
			return fmt.Errorf("decode turn/start: %w", err)
		}
		projection.turnOpen = true
		projection.turn = boundary.Turn
		projection.stepOpen = false
	case session.TurnEndEventName:
		projection.turnOpen = false
		projection.stepOpen = false
	case session.StepStartEventName:
		var boundary session.StepPosition
		if err := json.Unmarshal(committed.Data, &boundary); err != nil {
			return fmt.Errorf("decode step/start: %w", err)
		}
		projection.stepOpen = true
		projection.stepTurn = boundary.Turn
		projection.step = boundary.Step
	case session.StepEndEventName:
		projection.stepOpen = false
	case session.RequestHeaderEventName:
		var header struct {
			Header struct {
				Config struct {
					Provider string `json:"provider"`
				} `json:"config"`
			} `json:"header"`
		}
		if err := json.Unmarshal(committed.Data, &header); err != nil {
			return fmt.Errorf("decode request/header: %w", err)
		}
		projection.provider = header.Header.Config.Provider
	case RetryEventName:
		record, err := DecodeRetryRecord(committed.Data)
		if err != nil {
			return err
		}
		return projection.addDecodedRetry(record)
	case RetryStartedEventName:
		transition, err := DecodeRetryStarted(committed.Data)
		if err != nil {
			return err
		}
		return projection.addDecodedStarted(transition)
	}
	return nil
}

func (projection *historyProjection) addRetry(entry RetryRecord) error {
	facts, err := factsFromRecord(entry)
	if err != nil {
		return err
	}
	if err := validateRetryFacts(facts); err != nil {
		return err
	}
	return projection.addRetryFacts(entry, facts)
}

func (projection *historyProjection) addDecodedRetry(entry RetryRecord) error {
	facts, err := factsFromRecord(entry)
	if err != nil {
		return err
	}
	return projection.addRetryFacts(entry, facts)
}

func (projection *historyProjection) addRetryFacts(entry RetryRecord, facts retryFacts) error {
	if !projection.turnOpen {
		return errors.New("llm/retry must be appended inside an open turn")
	}
	if facts.turn != projection.turn {
		return fmt.Errorf("llm/retry names turn %d, but the open turn is %d", facts.turn, projection.turn)
	}
	if !projection.stepOpen {
		return errors.New("llm/retry must be appended inside an open step")
	}
	if facts.turn != projection.stepTurn || facts.step != projection.step {
		return fmt.Errorf(
			"llm/retry names turn %d/step %d, but the open step is %d/%d",
			facts.turn, facts.step, projection.stepTurn, projection.step,
		)
	}
	if projection.provider != facts.provider {
		return fmt.Errorf(
			"llm/retry provider %q does not match the failed request provider %q",
			facts.provider, projection.provider,
		)
	}
	chainIdentity := retryChainKey{
		turn: facts.turn, step: facts.step, provider: facts.provider, policyKey: facts.policyKey,
	}
	prior, exists := projection.chains[chainIdentity]
	expectedRetry := int64(1)
	if exists {
		expectedRetry = prior.retry + 1
	}
	if facts.retry != expectedRetry {
		return fmt.Errorf("llm/retry retry %d must equal provider policy retry %d", facts.retry, expectedRetry)
	}
	if exists {
		if prior.chainID != facts.chainID {
			return errors.New("llm/retry must preserve retryId across one provider-policy chain")
		}
	} else if ownerIdentity, used := projection.chainOwners[facts.chainID]; used && ownerIdentity != chainIdentity {
		return fmt.Errorf("llm/retry retryId %q is already owned by another chain", facts.chainID)
	}
	projection.chains[chainIdentity] = retryChainState{
		chainID: facts.chainID, retry: facts.retry, latest: entry,
	}
	projection.chainOwners[facts.chainID] = chainIdentity
	projection.scheduled[scheduledAttemptKey{chainID: facts.chainID, retry: facts.retry}] = scheduledAttempt{
		turn: facts.turn, step: facts.step,
	}
	return nil
}

func (projection *historyProjection) addStarted(transition RetryStarted) error {
	if err := validateStartedShape(transition); err != nil {
		return err
	}
	return projection.addStartedTransition(transition)
}

func (projection *historyProjection) addDecodedStarted(transition RetryStarted) error {
	return projection.addStartedTransition(transition)
}

func (projection *historyProjection) addStartedTransition(transition RetryStarted) error {
	attemptIdentity := scheduledAttemptKey{chainID: transition.RetryID, retry: transition.Retry}
	scheduled, exists := projection.scheduled[attemptIdentity]
	if !exists {
		return errors.New("llm/retry-started pairs no prior scheduled attempt")
	}
	if scheduled.turn != transition.Turn || scheduled.step != transition.Step {
		return errors.New("llm/retry-started turn/step must match its scheduled attempt")
	}
	if _, duplicate := projection.started[attemptIdentity]; duplicate {
		return errors.New("llm/retry-started repeats one scheduled attempt")
	}
	projection.started[attemptIdentity] = struct{}{}
	return nil
}

func (projection *historyProjection) priorRetry(chainIdentity retryChainKey) (RetryRecord, bool) {
	state, found := projection.chains[chainIdentity]
	if !found {
		return nil, false
	}
	return state.latest, true
}
