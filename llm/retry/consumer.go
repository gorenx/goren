package llmretry

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"math"
	mathrand "math/rand/v2"
	"time"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/session"
)

// PluginName is the canonical Harness Plugin name.
const PluginName = "@deepseek-ai/dsh-llm-retry"

// RuntimeOptions supplies non-serializable process hooks for deterministic
// tests and contained diagnostic reporting.
type RuntimeOptions struct {
	Random        func() float64
	NewRetryID    func() (RetryID, error)
	ObserverError func(error)
}

// Plugin owns the provider-routed request-error Middleware. Runtime owns its
// dispatch admission, lifetime cancellation, and quiescent teardown.
type Plugin struct {
	plugin.Base

	randomSample func() float64
	mintID       func() (RetryID, error)
	report       func(error)
}

// New constructs an inactive provider-routed retry Plugin.
func New(options RuntimeOptions) *Plugin {
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
	return &Plugin{
		randomSample: randomSample,
		mintID:       mintID,
		report:       reporter,
	}
}

// Manifest declares the source-compatible Agent Registry lifecycle dependency
// and request-error Middleware binding.
func (owner *Plugin) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: PluginName,
		Waterfalls: []plugin.WaterfallMiddlewareBinding{
			plugin.WaterfallOf(owner),
		},
	}
}

// Apply validates startup cancellation before Middleware publication.
func (*Plugin) Apply(requestContext context.Context) error {
	return requestContext.Err()
}

// Dispose is empty because Runtime cancels Lifetime and drains admitted
// Middleware invocations before calling it.
func (*Plugin) Dispose(context.Context) error {
	return nil
}

// Intercept executes provider-routed request recovery around downstream
// Middleware.
func (owner *Plugin) Intercept(
	requestContext context.Context,
	notice agent.RequestErrorNotice,
	downstream plugin.WaterfallAction[agent.RequestErrorNotice, agent.RequestErrorAction],
) (agent.RequestErrorAction, error) {
	return owner.resolve(requestContext, notice, downstream)
}

func (owner *Plugin) resolve(
	requestContext context.Context,
	notice agent.RequestErrorNotice,
	downstream plugin.WaterfallAction[agent.RequestErrorNotice, agent.RequestErrorAction],
) (agent.RequestErrorAction, error) {
	if requestContext == nil || downstream == nil {
		return agent.RequestErrorAction{}, errors.New("llm-retry: Context and downstream recovery are required")
	}
	if notice.RetryPolicy == nil {
		return downstream.Execute(requestContext, notice)
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
		action, downstreamErr := downstream.Execute(requestContext, notice)
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
		return downstream.Execute(requestContext, notice)
	}
	return owner.schedule(requestContext, notice, normalPolicy, downstream)
}

func (owner *Plugin) schedule(
	requestContext context.Context,
	notice agent.RequestErrorNotice,
	resolved llm.RetryPolicy,
	downstream plugin.WaterfallAction[agent.RequestErrorNotice, agent.RequestErrorAction],
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
		return downstream.Execute(requestContext, notice)
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
		return downstream.Execute(requestContext, notice)
	}
	return owner.backoff(
		requestContext, conversation, notice, resolved, policyIdentity, retryNumber, chainID, delayMS, projection,
	)
}

func (owner *Plugin) selectDelay(
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

func (owner *Plugin) backoff(
	requestContext context.Context,
	conversation session.Context,
	notice agent.RequestErrorNotice,
	resolved llm.RetryPolicy,
	policyIdentity string,
	retryNumber int64,
	chainID RetryID,
	delayMS float64,
	projection *historyProjection,
) (agent.RequestErrorAction, error) {
	if requestContext.Err() != nil {
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
	scheduledDraft, err := session.NewEventDraft(retryScheduledEvent, record)
	if err != nil {
		return agent.RequestErrorAction{}, err
	}
	if _, err := conversation.Commit(requestContext, session.Batch(scheduledDraft)); err != nil {
		return agent.RequestErrorAction{}, err
	}
	if requestContext.Err() != nil {
		return agent.RequestErrorAction{}, nil
	}
	completed := waitDelay(requestContext, delayMS)
	if !completed || requestContext.Err() != nil {
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
	startedDraft, err := session.NewEventDraft(retryStartedEvent, transition)
	if err != nil {
		return agent.RequestErrorAction{}, err
	}
	if _, err := conversation.Commit(requestContext, session.Batch(startedDraft)); err != nil {
		return agent.RequestErrorAction{}, err
	}
	return agent.RequestErrorAction{Retry: true}, nil
}

func (owner *Plugin) cancelled(requestContext context.Context) bool {
	return requestContext.Err() != nil
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
