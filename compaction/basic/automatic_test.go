package basic

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/compaction/toolresultpruner"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/llm/tokenmeter"
	"github.com/gorenx/goren/session"
)

func TestAutomaticPressureContainsFailureAndDeduplicatesTargetWarning(t *testing.T) {
	t.Parallel()
	runtimeValue := newRuntimeStub("checkpoint", 1_000)
	runtimeValue.resolved.Context = nil
	implementation := newBoundCompaction(
		t,
		Config{
			PolicyConfig: PolicyConfig{
				ThresholdRatio: floatPointer(0.5),
				RetainTokens:   int64Pointer(100),
			},
		},
		runtimeValue,
		&meterStub{},
		&liveStoreStub{},
		nil,
	)
	conversation := conversationFixture(t, 3, "pressure")
	subject := &agentStub{
		identifier:   conversation.ID(),
		conversation: conversation,
	}
	reported := make([]error, 0, 1)
	automation := newAutomaticFixture(implementation, func(problem error) {
		reported = append(reported, problem)
	})
	downstreamCalls := 0
	downstream := agent.PreStepActionFunc(func(
		context.Context,
		agent.PreStepNotice,
	) (agent.PreStepDecision, error) {
		downstreamCalls++
		return agent.PreStepDecision{
			Kind: agent.PreStepEnter,
		}, nil
	})
	for attempt := 0; attempt < 2; attempt++ {
		decision, err := automation.interceptPressure(
			context.Background(),
			agent.PreStepNotice{
				Subject: subject,
			},
			downstream,
		)
		if err != nil || decision.Kind != agent.PreStepEnter {
			t.Fatalf("pressure decision = %#v, error = %v", decision, err)
		}
	}
	if downstreamCalls != 2 || len(reported) != 1 ||
		!strings.Contains(reported[0].Error(), "no context capacity") {
		t.Fatalf("downstream calls = %d, reported = %#v", downstreamCalls, reported)
	}
}

func TestAutomaticOverflowRetriesOnlyAfterDurableProgressAndHonorsCap(t *testing.T) {
	t.Parallel()
	implementation := newBoundCompaction(
		t,
		Config{
			PolicyConfig: PolicyConfig{
				MaxOverflowRetries: intPointer(1),
			},
		},
		newRuntimeStub("checkpoint", 1_000),
		&meterStub{},
		&liveStoreStub{},
		nil,
	)
	conversation := conversationFixture(t, 3, "overflow")
	subject := &agentStub{
		identifier:   conversation.ID(),
		conversation: conversation,
	}
	automation := newAutomaticFixture(implementation, func(error) {})
	downstreamCalls := 0
	downstream := agent.RequestErrorActionFunc(func(
		context.Context,
		agent.RequestErrorNotice,
	) (agent.RequestErrorAction, error) {
		downstreamCalls++
		return agent.RequestErrorAction{}, nil
	})
	notice := agent.RequestErrorNotice{
		Subject: subject,
		Failure: llm.LlmFailure{
			Message: "context overflow",
			Code:    llm.ContextWindowExceededCode,
		},
	}
	action, err := automation.interceptOverflow(
		context.Background(),
		notice,
		downstream,
	)
	if err != nil || !action.Retry || downstreamCalls != 0 ||
		conversation.Surface().ReplaceGeneration != 1 {
		t.Fatalf(
			"first overflow action = %#v, error = %v, downstream = %d, Surface = %#v",
			action,
			err,
			downstreamCalls,
			conversation.Surface(),
		)
	}
	action, err = automation.interceptOverflow(
		context.Background(),
		notice,
		downstream,
	)
	if err != nil || action.Retry || downstreamCalls != 1 {
		t.Fatalf("capped action = %#v, error = %v, downstream = %d", action, err, downstreamCalls)
	}
}

func TestAutomaticOverflowFailureRetriesAfterPrunerProgress(t *testing.T) {
	t.Parallel()
	conversation := conversationFixture(t, 3, "overflow")
	prunerValue := &prunerStub{
		prune: func(
			_ context.Context,
			current session.Context,
		) (toolresultpruner.Result, error) {
			before := current.Surface()
			replacement, err := llm.NewUserMessage(llm.UserMessageInput{
				Content: []llm.ContentBlock{
					llm.NewTextBlock("model-free replacement"),
				},
				Source: llm.PluginMessageSource{
					Plugin: "fixture-pruner",
				},
			})
			if err != nil {
				return toolresultpruner.Result{}, err
			}
			sources := []int64{
				before.Nodes[0],
			}
			{
				var writeErr error
				draft, draftErr := session.NewSurfaceEventDraft(session.UserMessageAdded,
					replacement,
					session.SurfaceIntent{
						Operation: session.SurfaceReplace(
							before.Nodes[0],
							before.Nodes[0],
						),
						SourceEventSeqs: &sources,
					})
				writeErr = draftErr
				if draftErr == nil {
					_, commitErr := current.Commit(context.Background(), session.Batch(draft))
					writeErr = commitErr
				}
				err = writeErr
			}

			return toolresultpruner.Result{}, err
		},
	}
	runtimeValue := newRuntimeStub("checkpoint", 1_000)
	runtimeValue.streamErr = errors.New("summary provider unavailable")
	implementation := newBoundCompaction(
		t,
		Config{},
		runtimeValue,
		&meterStub{},
		&liveStoreStub{},
		prunerValue,
	)
	subject := &agentStub{
		identifier:   conversation.ID(),
		conversation: conversation,
	}
	reported := make([]error, 0, 1)
	automation := newAutomaticFixture(implementation, func(problem error) {
		reported = append(reported, problem)
	})
	downstreamCalls := 0
	action, err := automation.interceptOverflow(
		context.Background(),
		agent.RequestErrorNotice{
			Subject: subject,
			Failure: llm.LlmFailure{
				Message: "context overflow",
				Code:    llm.ContextWindowExceededCode,
			},
		},
		agent.RequestErrorActionFunc(func(
			context.Context,
			agent.RequestErrorNotice,
		) (agent.RequestErrorAction, error) {
			downstreamCalls++
			return agent.RequestErrorAction{}, nil
		}),
	)
	if err != nil || !action.Retry || downstreamCalls != 0 ||
		conversation.Surface().ReplaceGeneration != 1 || len(reported) != 1 ||
		!strings.Contains(reported[0].Error(), "summary provider unavailable") {
		t.Fatalf(
			"action = %#v, error = %v, downstream = %d, Surface = %#v, reported = %#v",
			action,
			err,
			downstreamCalls,
			conversation.Surface(),
			reported,
		)
	}
}

func TestAutomaticOverflowCancellationWinsOverPrunerProgress(t *testing.T) {
	t.Parallel()
	conversation := conversationFixture(t, 3, "overflow")
	requestContext, cancelRequest := context.WithCancelCause(
		context.Background(),
	)
	prunerValue := &prunerStub{
		prune: func(
			_ context.Context,
			current session.Context,
		) (toolresultpruner.Result, error) {
			before := current.Surface()
			replacement, err := llm.NewUserMessage(llm.UserMessageInput{
				Content: []llm.ContentBlock{
					llm.NewTextBlock("pruned"),
				},
				Source: llm.PluginMessageSource{
					Plugin: "fixture-pruner",
				},
			})
			if err != nil {
				return toolresultpruner.Result{}, err
			}
			sources := []int64{
				before.Nodes[0],
			}
			{
				var writeErr error
				draft, draftErr := session.NewSurfaceEventDraft(session.UserMessageAdded,
					replacement,
					session.SurfaceIntent{
						Operation: session.SurfaceReplace(
							before.Nodes[0],
							before.Nodes[0],
						),
						SourceEventSeqs: &sources,
					})
				writeErr = draftErr
				if draftErr == nil {
					_, commitErr := current.Commit(context.Background(), session.Batch(draft))
					writeErr = commitErr
				}
				err = writeErr
			}

			return toolresultpruner.Result{}, err
		},
	}
	runtimeValue := newRuntimeStub("checkpoint", 1_000)
	runtimeValue.beforeCall = func() {
		cancelRequest(errors.New("caller cancelled"))
	}
	implementation := newBoundCompaction(
		t,
		Config{},
		runtimeValue,
		&meterStub{},
		&liveStoreStub{},
		prunerValue,
	)
	subject := &agentStub{
		identifier:   conversation.ID(),
		conversation: conversation,
	}
	downstreamCalls := 0
	action, err := newAutomaticFixture(implementation, func(error) {}).interceptOverflow(
		requestContext,
		agent.RequestErrorNotice{
			Subject: subject,
			Failure: llm.LlmFailure{
				Message: "context overflow",
				Code:    llm.ContextWindowExceededCode,
			},
		},
		agent.RequestErrorActionFunc(func(
			context.Context,
			agent.RequestErrorNotice,
		) (agent.RequestErrorAction, error) {
			downstreamCalls++
			return agent.RequestErrorAction{}, nil
		}),
	)
	if err != nil || action.Retry || downstreamCalls != 1 ||
		conversation.Surface().ReplaceGeneration != 1 {
		t.Fatalf(
			"cancelled action = %#v, error = %v, downstream = %d, Surface = %#v",
			action,
			err,
			downstreamCalls,
			conversation.Surface(),
		)
	}
}

func TestAutomaticOverflowStateResetsOnAssistantSuccessAndIdle(t *testing.T) {
	t.Parallel()
	implementation := newBoundCompaction(
		t,
		Config{},
		newRuntimeStub("checkpoint", 1_000),
		&meterStub{},
		&liveStoreStub{},
		nil,
	)
	conversation := conversationFixture(t, 2, "overflow")
	subject := &agentStub{
		identifier:   conversation.ID(),
		conversation: conversation,
	}
	automation := newAutomaticFixture(implementation, func(error) {})
	automation.recordRetry(subject, 1)
	assistantOutput, err := llm.NewAssistantMessage(llm.AssistantMessageInput{
		Content: []llm.ContentBlock{
			llm.NewTextBlock("success"),
		},
		Source: llm.ModelMessageSource{
			Provider: fixtureProvider,
			Model:    fixtureModel,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	committed, err := session.Event{}, error(nil)
	{
		var committedEvent session.Event
		var writeErr error
		draft, draftErr := session.NewSurfaceEventDraft(session.AssistantMessaged,
			session.AssistantMessage{
				Turn:    3,
				Step:    1,
				Message: assistantOutput,
			},
			session.SurfaceIntent{
				Operation: session.SurfaceAppend(),
			})
		writeErr = draftErr
		if draftErr == nil {
			receipt, commitErr := conversation.Commit(context.Background(), session.Batch(draft))
			writeErr = commitErr
			if commitErr == nil {
				committedEvent = receipt.Events[0]
			}
		}
		committed = committedEvent
		err = writeErr
	}

	if err != nil {
		t.Fatal(err)
	}
	if err := automation.observeEvent(
		context.Background(),
		session.EventAppended{
			Conversation: conversation,
			Committed:    committed,
		},
	); err != nil {
		t.Fatal(err)
	}
	if retries := automation.retryCount(subject); retries != 0 {
		t.Fatalf("retries after assistant success = %d", retries)
	}
	automation.recordRetry(subject, 1)
	if err := automation.observeEvent(
		context.Background(),
		agent.StatusChanged{
			Subject: subject,
			Status:  agent.StatusIdle,
		},
	); err != nil {
		t.Fatal(err)
	}
	if retries := automation.retryCount(subject); retries != 0 {
		t.Fatalf("retries after idle = %d", retries)
	}
}

func TestAutomaticOverflowIgnoresNonCanonicalFailure(t *testing.T) {
	t.Parallel()
	implementation := newBoundCompaction(
		t,
		Config{},
		newRuntimeStub("checkpoint", 1_000),
		&meterStub{},
		&liveStoreStub{},
		nil,
	)
	conversation := conversationFixture(t, 2, "overflow")
	subject := &agentStub{
		identifier:   conversation.ID(),
		conversation: conversation,
	}
	downstreamCalls := 0
	action, err := newAutomaticFixture(implementation, func(error) {}).interceptOverflow(
		context.Background(),
		agent.RequestErrorNotice{
			Subject: subject,
			Failure: llm.LlmFailure{
				Message: "server failed",
				Code:    "SERVER",
			},
		},
		agent.RequestErrorActionFunc(func(
			context.Context,
			agent.RequestErrorNotice,
		) (agent.RequestErrorAction, error) {
			downstreamCalls++
			return agent.RequestErrorAction{}, nil
		}),
	)
	if err != nil || action.Retry || downstreamCalls != 1 ||
		conversation.Surface().ReplaceGeneration != 0 {
		t.Fatalf(
			"noncanonical action = %#v, error = %v, downstream = %d, Surface = %#v",
			action,
			err,
			downstreamCalls,
			conversation.Surface(),
		)
	}
}

func newAutomaticFixture(
	implementation *Compaction,
	reporter func(error),
) *automaticCompaction {
	return &automaticCompaction{
		engine:           implementation,
		report:           reporter,
		overflowSequence: make(map[session.Context]overflowRecovery),
		warnedTargets:    make(map[string]struct{}),
	}
}

var _ tokenmeter.Meter = (*meterStub)(nil)
