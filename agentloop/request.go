package agentloop

import (
	"context"
	"errors"
	"fmt"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/systemprompt"
)

// modelRequester owns request reconstruction, LLM attempt execution, and the
// retry seam for one Agent. It delegates Tool-call scheduling after a complete
// Assistant message has committed.
type modelRequester struct {
	subject             *ReactLoopAgent
	models              llm.LlmRuntime
	toolCalls           *toolCallExecutor
	requestHeaderLogged bool
}

func newModelRequester(
	subject *ReactLoopAgent,
	models llm.LlmRuntime,
	toolCalls *toolCallExecutor,
) *modelRequester {
	return &modelRequester{
		subject:   subject,
		models:    models,
		toolCalls: toolCalls,
	}
}

func (requester *modelRequester) deactivate() {
	if requester.toolCalls != nil {
		requester.toolCalls.deactivate()
	}
	requester.toolCalls = nil
	requester.models = nil
}

type requestAttempt struct {
	options  llm.GenerateOptions
	prepared llm.PreparedLlmCall
}

func (requester *modelRequester) executeStep(
	requestContext context.Context,
	turn int64,
	step int64,
	prepared preparedStep,
) (session.TurnEndReason, error) {
	systemText, err := systemprompt.RenderPrompt(prepared.assembly)
	if err != nil {
		return nil, err
	}
	for {
		if err := contextFailure(requestContext); err != nil {
			return nil, err
		}
		boundaryMessages, err := requester.subject.conversation.DeriveMessages()
		if err != nil {
			return nil, err
		}
		attempt, err := requester.buildRequest(
			requestContext,
			turn,
			step,
			prepared.assembly.Tools,
			systemText,
			boundaryMessages,
		)
		if err != nil {
			return nil, err
		}
		assembler := llm.NewBlockAssembler()
		chunkSequences := make([]int64, 0)
		var stream llm.ChunkStream
		if attempt.prepared != nil {
			stream, err = attempt.prepared.Stream(requestContext, attempt.options)
		} else {
			stream, err = requester.models.Stream(requestContext, attempt.options)
		}
		if err != nil {
			return nil, err
		}
		for {
			if err := contextFailure(requestContext); err != nil {
				_ = stream.Close(context.Background())
				return nil, err
			}
			chunk, found, nextErr := stream.Next(requestContext)
			if nextErr != nil {
				_ = stream.Close(context.Background())
				return nil, nextErr
			}
			if !found {
				break
			}
			committed, appendErr := session.AppendSerialized(
				requester.subject.conversation,
				session.AssistantChunked,
				session.AssistantChunk{
					Turn:  turn,
					Step:  step,
					Chunk: chunk,
				},
			)
			if appendErr != nil {
				_ = stream.Close(context.Background())
				return nil, appendErr
			}
			chunkSequences = append(chunkSequences, committed.Seq)
			if err := assembler.Push(chunk); err != nil {
				_ = stream.Close(context.Background())
				return nil, err
			}
		}
		if err := stream.Close(context.Background()); err != nil {
			return nil, err
		}
		if err := contextFailure(requestContext); err != nil {
			return nil, err
		}
		finishReason := assembler.FinishValue()
		if failure, failed := finishFailure(finishReason); failed {
			var retryPolicy llm.RetryPolicy
			if attempt.prepared != nil {
				retryPolicy = attempt.prepared.RetryPolicyValue()
			}
			action, resolveErr := agent.ResolveRequestError(
				requestContext,
				agent.RequestErrorNotice{
					Subject:     requester.subject,
					Turn:        turn,
					Step:        step,
					Provider:    attempt.options.Provider,
					Failure:     failure,
					RetryPolicy: retryPolicy,
				},
				agent.RequestErrorActionFunc(func(
					context.Context,
					agent.RequestErrorNotice,
				) (agent.RequestErrorAction, error) {
					return agent.RequestErrorAction{}, nil
				}),
			)
			if resolveErr != nil {
				return nil, resolveErr
			}
			if err := contextFailure(requestContext); err != nil {
				return nil, err
			}
			if action.Retry {
				continue
			}
			return nil, llmErrorFromFailure(failure)
		}

		blocks, err := assembler.AssembledBlocks()
		if err != nil {
			return nil, err
		}
		assistantReply, err := llm.NewAssistantMessage(llm.AssistantMessageInput{
			Content: blocks,
			Source: llm.ModelMessageSource{
				Provider:    attempt.options.Provider,
				Model:       attempt.options.Model,
				ReplayState: assembler.ReplayValue(),
			},
		})
		if err != nil {
			return nil, err
		}
		assembledPayload := session.AssistantMessage{
			Turn:    turn,
			Step:    step,
			Message: assistantReply,
		}
		if usage, present := assembler.UsageValue(); present {
			assembledPayload.Usage = &usage
		}
		if _, err := session.AppendSurfaceSerialized(
			requester.subject.conversation,
			session.AssistantMessaged,
			assembledPayload,
			session.SurfaceIntent{
				Operation:       session.SurfaceAppend(),
				SourceEventSeqs: &chunkSequences,
			},
		); err != nil {
			return nil, err
		}
		if finishReason.ReasonKind() == "max-tokens" {
			return session.TurnMaxTokens{}, nil
		}
		toolCalls := assistantToolCalls(assistantReply)
		if len(toolCalls) == 0 {
			return session.TurnCompleted{}, nil
		}
		concluded, err := requester.toolCalls.execute(
			requestContext,
			turn,
			step,
			toolCalls,
		)
		if err != nil {
			return nil, err
		}
		if concluded {
			return session.TurnCompleted{}, nil
		}
		return nil, nil
	}
}

func (requester *modelRequester) buildRequest(
	requestContext context.Context,
	turn int64,
	step int64,
	toolSchemas []llm.ToolSchema,
	systemText string,
	boundaryMessages []llm.Message,
) (requestAttempt, error) {
	persistedHeader, headerFound, err := requester.subject.conversation.RequestHeaderValue()
	if err != nil {
		return requestAttempt{}, err
	}
	loopOptions := requester.subject.OptionsValue()
	seedConfig := llm.CallConfig{
		Provider:  loopOptions.Provider,
		Model:     loopOptions.Model,
		MaxTokens: cloneInt(loopOptions.MaxTokens),
	}
	if headerFound && persistedHeader.Config.Provider == seedConfig.Provider && persistedHeader.Config.Model == seedConfig.Model &&
		(persistedHeader.AdapterDefaults == nil || !persistedHeader.AdapterDefaults.ReasoningEffort) {
		seedConfig.ReasoningEffort = persistedHeader.Config.ReasoningEffort
	}
	if requester.requestHeaderLogged && headerFound {
		seedConfig = requestProposal(persistedHeader)
	}
	proposed, err := agent.ResolveRequest(
		requestContext,
		agent.RequestNotice{
			Subject: requester.subject,
			Turn:    turn,
			Step:    step,
		},
		agent.RequestActionFunc(func(
			context.Context,
			agent.RequestNotice,
		) (agent.RequestResolution, error) {
			return agent.RequestResolution{
				Config: llm.CloneCallConfig(seedConfig),
			}, nil
		}),
	)
	if err != nil {
		return requestAttempt{}, err
	}
	if err := contextFailure(requestContext); err != nil {
		return requestAttempt{}, err
	}
	if proposed.Provider == "" || proposed.Model == "" {
		return requestAttempt{}, fmt.Errorf(
			"agentloop: Agent %q has no provider/model; set Agent Options or supply both through agent/request",
			requester.subject.identifier,
		)
	}
	var preparedCall llm.PreparedLlmCall
	effective := proposed
	preparedCall, err = requester.models.PrepareCall(requestContext, proposed)
	if err != nil {
		var llmProblem *llm.LlmError
		if !errors.As(err, &llmProblem) || llmProblem.Code() != "NO_ADAPTER" {
			return requestAttempt{}, err
		}
		preparedCall = nil
	} else {
		effective = preparedCall.ConfigValue()
	}
	if err := contextFailure(requestContext); err != nil {
		return requestAttempt{}, err
	}
	headerSnapshot := session.EpochHeader{
		Config: effective,
		Tools:  toolSchemas,
	}
	if preparedCall != nil {
		defaults := preparedCall.AdapterDefaultsValue()
		headerSnapshot.AdapterDefaults = &defaults
	}
	if systemText != "" {
		headerSnapshot.System = &systemText
	}
	headerSnapshot = session.CanonicalEpochHeader(headerSnapshot)
	baseline, baselineFound, err := requester.subject.conversation.RequestHeaderValue()
	if err != nil {
		return requestAttempt{}, err
	}
	if !requester.requestHeaderLogged {
		reason := session.RequestHeaderInitial
		if baselineFound {
			reason = session.RequestHeaderResume
		}
		if _, err := session.AppendSerialized(
			requester.subject.conversation,
			session.RequestHeaderSet,
			session.RequestHeaderSnapshot{
				Header: headerSnapshot,
				Reason: reason,
			},
		); err != nil {
			return requestAttempt{}, err
		}
		requester.requestHeaderLogged = true
	} else if !baselineFound || !session.EpochHeaderEqual(baseline, headerSnapshot) {
		if _, err := session.AppendSerialized(
			requester.subject.conversation,
			session.RequestHeaderSet,
			session.RequestHeaderSnapshot{
				Header: headerSnapshot,
				Reason: session.RequestHeaderChange,
			},
		); err != nil {
			return requestAttempt{}, err
		}
	}

	routeContext := session.RequestRouteContext{
		Provider: effective.Provider,
		Model:    effective.Model,
	}
	if preparedCall != nil {
		if modelContext, present := preparedCall.ContextValue(); present {
			contextWindow := modelContext.ContextWindow
			routeContext.ContextWindow = &contextWindow
		}
	}
	previousContext, contextFound, err := requester.subject.conversation.RequestContextValue()
	if err != nil {
		return requestAttempt{}, err
	}
	if !contextFound || !sameRequestContext(previousContext, routeContext) {
		if _, err := session.AppendSerialized(
			requester.subject.conversation,
			session.RequestContextSet,
			routeContext,
		); err != nil {
			return requestAttempt{}, err
		}
	}
	if err := contextFailure(requestContext); err != nil {
		return requestAttempt{}, err
	}
	requestOptions := llm.GenerateOptions{
		CallConfig: headerSnapshot.Config,
		Messages:   boundaryMessages,
		System:     headerSnapshot.System,
		Tools:      headerSnapshot.Tools,
		SessionID:  string(requester.subject.identifier),
	}
	detached, err := llm.CloneGenerateOptions(requestOptions)
	if err != nil {
		return requestAttempt{}, err
	}
	return requestAttempt{
		options:  detached,
		prepared: preparedCall,
	}, nil
}

func requestProposal(headerSnapshot session.EpochHeader) llm.CallConfig {
	proposal := llm.CloneCallConfig(headerSnapshot.Config)
	if headerSnapshot.AdapterDefaults == nil {
		return proposal
	}
	if headerSnapshot.AdapterDefaults.ReasoningEffort {
		proposal.ReasoningEffort = ""
	}
	if headerSnapshot.AdapterDefaults.MaxTokens {
		proposal.MaxTokens = nil
	}
	return proposal
}

func sameRequestContext(left session.RequestRouteContext, right session.RequestRouteContext) bool {
	if left.Provider != right.Provider || left.Model != right.Model {
		return false
	}
	if left.ContextWindow == nil || right.ContextWindow == nil {
		return left.ContextWindow == nil && right.ContextWindow == nil
	}
	return *left.ContextWindow == *right.ContextWindow
}

func finishFailure(reason llm.FinishReason) (llm.LlmFailure, bool) {
	switch selected := reason.(type) {
	case llm.ErrorFinish:
		return selected.Failure, true
	case *llm.ErrorFinish:
		return selected.Failure, true
	case llm.AbortedFinish:
		return selected.Failure, true
	case *llm.AbortedFinish:
		return selected.Failure, true
	default:
		return llm.LlmFailure{}, false
	}
}

func llmErrorFromFailure(failure llm.LlmFailure) error {
	problem, err := llm.NewLlmError(failure.Message, failure.Code, llm.LlmErrorOptions{
		Status:               failure.Status,
		ProviderRetryAfterMS: failure.ProviderRetryAfterMS,
		RequestID:            failure.RequestID,
	})
	if err != nil {
		return fmt.Errorf("agentloop: invalid LLM failure: %w", err)
	}
	return problem
}

func assistantToolCalls(reply llm.AssistantMessage) []llm.ToolCallBlock {
	blocks := reply.ContentValue()
	toolCalls := make([]llm.ToolCallBlock, 0)
	for _, block := range blocks {
		switch retained := block.(type) {
		case llm.ToolCallBlock:
			toolCalls = append(toolCalls, retained)
		case *llm.ToolCallBlock:
			if retained != nil {
				toolCalls = append(toolCalls, *retained)
			}
		}
	}
	return toolCalls
}
