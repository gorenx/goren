package llmretry

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"math"
	mathrand "math/rand/v2"
	"sync"
	"time"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/session"
)

// RuntimeOptions supplies non-serializable process hooks for deterministic
// tests and contained diagnostic reporting.
type RuntimeOptions struct {
	Random        func() float64
	NewRetryID    func() (RetryID, error)
	ObserverError func(error)
}

type retryConsumer struct {
	lifetime       context.Context
	cancelLifetime context.CancelFunc
	randomSample   func() float64
	mintID         func() (RetryID, error)
	report         func(error)

	mu              sync.Mutex
	closed          bool
	active          sync.WaitGroup
	releaseListener plugin.Disposer
}

// Install registers the default provider-routed request recovery listener and
// owns cancellation plus quiescent teardown in pluginScope.
func Install(
	requestContext context.Context,
	pluginScope *plugin.Scope,
	options RuntimeOptions,
) (plugin.Disposer, error) {
	if requestContext == nil || pluginScope == nil {
		return nil, errors.New("llm-retry: Context and Scope are required")
	}
	lifetime, cancelLifetime := context.WithCancel(context.Background())
	randomSample := options.Random
	if randomSample == nil {
		randomSample = mathrand.Float64
	}
	mintID := options.NewRetryID
	if mintID == nil {
		mintID = mintRetryID
	}
	reporter := options.ObserverError
	if reporter == nil {
		reporter = func(error) {}
	}
	owner := &retryConsumer{
		lifetime: lifetime, cancelLifetime: cancelLifetime,
		randomSample: randomSample, mintID: mintID, report: reporter,
	}
	releaseListener, err := agent.OnRequestError(pluginScope, owner.handle)
	if err != nil {
		cancelLifetime()
		return nil, err
	}
	owner.releaseListener = releaseListener
	releaseAll, err := plugin.Own(pluginScope, "llm-retry: abort and drain active recovery", owner.close)
	if err != nil {
		cancelLifetime()
		return nil, errors.Join(err, releaseListener(requestContext))
	}
	return releaseAll, nil
}

func (owner *retryConsumer) handle(
	requestContext context.Context,
	notice agent.RequestErrorNotice,
	downstream agent.RequestErrorNext,
) (agent.RequestErrorAction, error) {
	if !owner.begin() {
		return agent.RequestErrorAction{}, nil
	}
	defer owner.active.Done()
	return owner.resolve(requestContext, notice, downstream)
}

func (owner *retryConsumer) begin() bool {
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if owner.closed {
		return false
	}
	owner.active.Add(1)
	return true
}

func (owner *retryConsumer) close(closeContext context.Context) error {
	owner.mu.Lock()
	if owner.closed {
		owner.mu.Unlock()
		return nil
	}
	owner.closed = true
	releaseListener := owner.releaseListener
	owner.cancelLifetime()
	owner.mu.Unlock()
	var releaseErr error
	if releaseListener != nil {
		releaseErr = releaseListener(closeContext)
	}
	owner.active.Wait()
	return releaseErr
}

func (owner *retryConsumer) resolve(
	requestContext context.Context,
	notice agent.RequestErrorNotice,
	downstream agent.RequestErrorNext,
) (agent.RequestErrorAction, error) {
	if requestContext == nil || downstream == nil {
		return agent.RequestErrorAction{}, errors.New("llm-retry: Context and downstream recovery are required")
	}
	if notice.RetryPolicy == nil {
		return downstream(requestContext)
	}
	if notice.Subject == nil || notice.Subject.SessionValue() == nil {
		return agent.RequestErrorAction{}, errors.New("llm-retry: request-error Agent and Session are required")
	}

	var normalPolicy llm.NormalRetryPolicy
	switch configured := notice.RetryPolicy.(type) {
	case llm.AlwaysRetryPolicy:
		if owner.cancelled(requestContext) {
			return agent.RequestErrorAction{}, nil
		}
		action, downstreamErr := downstream(requestContext)
		if owner.cancelled(requestContext) {
			return agent.RequestErrorAction{}, nil
		}
		if downstreamErr != nil {
			owner.report(fmt.Errorf(
				"llm-retry: provider %q always policy ignored a downstream recovery failure: %w",
				notice.Provider, downstreamErr,
			))
		} else if action.Retry {
			return action, nil
		}
		return owner.schedule(requestContext, notice, configured, downstream)
	case llm.NormalRetryPolicy:
		normalPolicy = configured
	default:
		return agent.RequestErrorAction{}, errors.New("llm-retry: unsupported retry policy implementation")
	}
	if !retryable(normalPolicy, notice.Failure.Code) {
		return downstream(requestContext)
	}
	return owner.schedule(requestContext, notice, normalPolicy, downstream)
}

func (owner *retryConsumer) schedule(
	requestContext context.Context,
	notice agent.RequestErrorNotice,
	resolved llm.RetryPolicy,
	downstream agent.RequestErrorNext,
) (agent.RequestErrorAction, error) {
	conversation := notice.Subject.SessionValue()
	projection, err := analyzeHistory(conversation.Events())
	if err != nil {
		return agent.RequestErrorAction{}, err
	}
	policyIdentity, err := policyKey(resolved)
	if err != nil {
		return agent.RequestErrorAction{}, fmt.Errorf("llm-retry: encode policy key: %w", err)
	}
	chainIdentity := retryChainKey{
		turn:      notice.Turn,
		step:      notice.Step,
		provider:  notice.Provider,
		policyKey: policyIdentity,
	}
	prior, found := projection.priorRetry(chainIdentity)
	previousRetry := int64(0)
	chainID := RetryID("")
	if found {
		facts, factsErr := factsFromRecord(prior)
		if factsErr != nil {
			return agent.RequestErrorAction{}, factsErr
		}
		previousRetry = facts.retry
		chainID = facts.chainID
	}
	if bounded, normal := resolved.(llm.NormalRetryPolicy); normal && previousRetry >= bounded.MaxRetries {
		return downstream(requestContext)
	}
	retryNumber := previousRetry + 1
	if chainID == "" {
		chainID, err = owner.mintID()
		if err != nil {
			return agent.RequestErrorAction{}, fmt.Errorf("llm-retry: mint retry id: %w", err)
		}
		if chainID == "" {
			return agent.RequestErrorAction{}, errors.New("llm-retry: minted retry id is empty")
		}
	}
	delayMS, delegate, err := owner.selectDelay(notice.Failure, resolved, retryNumber)
	if err != nil {
		return agent.RequestErrorAction{}, err
	}
	if delegate {
		return downstream(requestContext)
	}
	return owner.backoff(
		requestContext, conversation, notice, resolved, policyIdentity, retryNumber, chainID, delayMS, projection,
	)
}

func (owner *retryConsumer) selectDelay(
	problem llm.LlmFailure,
	resolved llm.RetryPolicy,
	retryNumber int64,
) (float64, bool, error) {
	delayPolicy := resolved.RetryBackoff()
	if problem.ProviderRetryAfterMS != nil && !math.IsNaN(*problem.ProviderRetryAfterMS) &&
		!math.IsInf(*problem.ProviderRetryAfterMS, 0) && *problem.ProviderRetryAfterMS > 0 {
		if *problem.ProviderRetryAfterMS <= delayPolicy.MaxDelayMS {
			return *problem.ProviderRetryAfterMS, false, nil
		}
		if _, normal := resolved.(llm.NormalRetryPolicy); normal {
			return 0, true, nil
		}
	}
	delayMS, err := localDelay(resolved, retryNumber, owner.randomSample)
	return delayMS, false, err
}

func (owner *retryConsumer) backoff(
	requestContext context.Context,
	conversation *session.Session,
	notice agent.RequestErrorNotice,
	resolved llm.RetryPolicy,
	policyIdentity string,
	retryNumber int64,
	chainID RetryID,
	delayMS float64,
	projection *historyProjection,
) (agent.RequestErrorAction, error) {
	delayContext, cancelDelay := context.WithCancel(requestContext)
	stopLifetime := context.AfterFunc(owner.lifetime, cancelDelay)
	defer func() {
		stopLifetime()
		cancelDelay()
	}()
	if delayContext.Err() != nil {
		return agent.RequestErrorAction{}, nil
	}

	var record RetryRecord
	switch configured := resolved.(type) {
	case llm.NormalRetryPolicy:
		record = NormalRetryRecord{
			RetryID: chainID, Turn: notice.Turn, Step: notice.Step, Provider: notice.Provider,
			Mode: llm.RetryNormal, PolicyKey: policyIdentity, Retry: retryNumber,
			MaxRetries: configured.MaxRetries, DelayMS: delayMS, Failure: cloneFailure(notice.Failure),
		}
	case llm.AlwaysRetryPolicy:
		record = AlwaysRetryRecord{
			RetryID: chainID, Turn: notice.Turn, Step: notice.Step, Provider: notice.Provider,
			Mode: llm.RetryAlways, PolicyKey: policyIdentity, Retry: retryNumber,
			DelayMS: delayMS, Failure: cloneFailure(notice.Failure),
		}
	default:
		return agent.RequestErrorAction{}, errors.New("llm-retry: unsupported retry policy implementation")
	}
	if err := projection.addRetry(record); err != nil {
		return agent.RequestErrorAction{}, err
	}
	if _, err := session.AppendSerialized(conversation, retryScheduledEvent, record); err != nil {
		return agent.RequestErrorAction{}, err
	}
	if delayContext.Err() != nil {
		return agent.RequestErrorAction{}, nil
	}
	completed := waitDelay(delayContext, delayMS)
	if !completed || delayContext.Err() != nil {
		return agent.RequestErrorAction{}, nil
	}
	transition := RetryStarted{RetryID: chainID, Turn: notice.Turn, Step: notice.Step, Retry: retryNumber}
	latestProjection, err := analyzeHistory(conversation.Events())
	if err != nil {
		return agent.RequestErrorAction{}, err
	}
	if err := latestProjection.addStarted(transition); err != nil {
		return agent.RequestErrorAction{}, err
	}
	if _, err := session.AppendSerialized(conversation, retryStartedEvent, transition); err != nil {
		return agent.RequestErrorAction{}, err
	}
	return agent.RequestErrorAction{Retry: true}, nil
}

func (owner *retryConsumer) cancelled(requestContext context.Context) bool {
	return requestContext.Err() != nil || owner.lifetime.Err() != nil
}

func waitDelay(requestContext context.Context, delayMS float64) bool {
	delay := time.Duration(delayMS * float64(time.Millisecond))
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-requestContext.Done():
		return false
	case <-timer.C:
		return true
	}
}

func mintRetryID() (RetryID, error) {
	var bytesValue [16]byte
	if _, err := rand.Read(bytesValue[:]); err != nil {
		return "", err
	}
	bytesValue[6] = (bytesValue[6] & 0x0f) | 0x40
	bytesValue[8] = (bytesValue[8] & 0x3f) | 0x80
	return RetryID(fmt.Sprintf(
		"%08x-%04x-%04x-%04x-%012x",
		bytesValue[0:4], bytesValue[4:6], bytesValue[6:8], bytesValue[8:10], bytesValue[10:16],
	)), nil
}
