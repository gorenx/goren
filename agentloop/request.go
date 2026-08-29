package agentloop

import (
	"context"
	"errors"
	"fmt"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/systemprompt"
)

// requestAttempt is one model call prepared from the durable Step boundary.
type requestAttempt struct {
	options  llm.GenerateOptions
	prepared llm.PreparedLlmCall
}

func (executor *stepExecutor) executeModel(
	requestContext context.Context,
	current *stepPlan,
) (session.TurnEndReason, error) {
	turn := current.position.Turn
	number := current.position.Step
	systemText, err := systemprompt.RenderPrompt(current.assembly)
	if err != nil {
		return nil, err
	}
	for {
		if err = contextFailure(requestContext); err != nil {
			return nil, err
		}
		boundaryMessages, err := executor.subject.conversation.DeriveMessages()
		if err != nil {
			return nil, err
		}
		attempt, err := executor.buildRequest(
			requestContext,
			turn,
			number,
			current.assembly.Tools,
			systemText,
			boundaryMessages,
		)
		if err != nil {
			return nil, err
		}
		assembler := llm.NewBlockAssembler()
		chunkSequences := make([]int64, 0)
		var chunkStream llm.ChunkStream
		if attempt.prepared != nil {
			chunkStream, err = attempt.prepared.Stream(requestContext, attempt.options)
		} else {
			chunkStream, err = executor.models.Stream(requestContext, attempt.options)
		}
		if err != nil {
			return nil, err
		}
		for {
			if err = contextFailure(requestContext); err != nil {
				_ = chunkStream.Close(context.Background())
				return nil, err
			}
			chunk, found, nextErr := chunkStream.Next(requestContext)
			if nextErr != nil {
				_ = chunkStream.Close(context.Background())
				return nil, nextErr
			}
			if !found {
				break
			}
			chunkDraft, appendErr := session.NewEventDraft(
				session.AssistantChunked,
				session.AssistantChunk{
					Turn:  turn,
					Step:  number,
					Chunk: chunk,
				},
			)
			if appendErr != nil {
				_ = chunkStream.Close(context.Background())
				return nil, appendErr
			}
			result, appendErr := executor.subject.conversation.Commit(
				requestContext,
				session.Batch(chunkDraft),
			)
			if appendErr != nil {
				_ = chunkStream.Close(context.Background())
				return nil, appendErr
			}
			committed := result.Events[0]
			chunkSequences = append(chunkSequences, committed.Seq)
			if err := assembler.Push(chunk); err != nil {
				_ = chunkStream.Close(context.Background())
				return nil, err
			}
		}
		if err = chunkStream.Close(context.Background()); err != nil {
			return nil, err
		}
		if err = contextFailure(requestContext); err != nil {
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
					Subject:     executor.subject,
					Turn:        turn,
					Step:        number,
					Provider:    attempt.options.Provider,
					Failure:     failure,
					RetryPolicy: retryPolicy,
				},
				noRetryRequestErrorHandler{},
			)
			if resolveErr != nil {
				return nil, resolveErr
			}
			if err = contextFailure(requestContext); err != nil {
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
		assistantReply, err := agentmessage.NewAssistantMessage(agentmessage.AssistantMessageInput{
			Content: blocks,
			Source: agentmessage.ModelMessageSource{
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
			Step:    number,
			Message: assistantReply,
		}
		if usage, present := assembler.UsageValue(); present {
			assembledPayload.Usage = &usage
		}
		messageDraft, err := session.NewSurfaceEventDraft(
			session.AssistantMessaged,
			assembledPayload,
			session.SurfaceIntent{
				Operation:       session.SurfaceAppend(),
				SourceEventSeqs: &chunkSequences,
			},
		)
		if err != nil {
			return nil, err
		}
		if _, err = executor.subject.conversation.Commit(requestContext, session.Batch(messageDraft)); err != nil {
			return nil, err
		}
		if finishReason.ReasonKind() == "max-tokens" {
			return session.TurnMaxTokens{}, nil
		}
		toolCalls := assistantToolCalls(assistantReply)
		if len(toolCalls) == 0 {
			return session.TurnCompleted{}, nil
		}
		concluded, err := executor.toolCalls.execute(
			requestContext,
			turn,
			number,
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

func (executor *stepExecutor) buildRequest(
	requestContext context.Context,
	turn int64,
	step int64,
	toolSchemas []llm.ToolSchema,
	systemText string,
	boundaryMessages []agentmessage.Message,
) (requestAttempt, error) {
	persistedHeader, err := session.LatestRequestHeader(
		executor.subject.conversation.Events(),
	)
	if err != nil {
		return requestAttempt{}, err
	}
	headerFound := persistedHeader != nil
	loopOptions := executor.subject.OptionsValue()
	seedConfig := llm.CallConfig{
		Provider:  loopOptions.Provider,
		Model:     loopOptions.Model,
		MaxTokens: cloneInt(loopOptions.MaxTokens),
	}
	if headerFound && persistedHeader.Config.Provider == seedConfig.Provider && persistedHeader.Config.Model == seedConfig.Model &&
		(persistedHeader.AdapterDefaults == nil || !persistedHeader.AdapterDefaults.ReasoningEffort) {
		seedConfig.ReasoningEffort = persistedHeader.Config.ReasoningEffort
	}
	if executor.requestHeaderLogged && headerFound {
		seedConfig = requestProposal(*persistedHeader)
	}
	proposed, err := agent.ResolveRequest(
		requestContext,
		agent.RequestNotice{
			Subject: executor.subject,
			Turn:    turn,
			Step:    step,
		},
		requestConfigAction{
			config: llm.CloneCallConfig(seedConfig),
		},
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
			executor.subject.identifier,
		)
	}
	var preparedCall llm.PreparedLlmCall
	effective := proposed
	preparedCall, err = executor.models.PrepareCall(requestContext, proposed)
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
	baseline, err := session.LatestRequestHeader(
		executor.subject.conversation.Events(),
	)
	if err != nil {
		return requestAttempt{}, err
	}
	baselineFound := baseline != nil
	if !executor.requestHeaderLogged {
		reason := session.RequestHeaderInitial
		if baselineFound {
			reason = session.RequestHeaderResume
		}
		headerDraft, err := session.NewEventDraft(
			session.RequestHeaderSet,
			session.RequestHeaderSnapshot{
				Header: headerSnapshot,
				Reason: reason,
			},
		)
		if err != nil {
			return requestAttempt{}, err
		}
		if _, err = executor.subject.conversation.Commit(requestContext, session.Batch(headerDraft)); err != nil {
			return requestAttempt{}, err
		}
		executor.requestHeaderLogged = true
	} else if !baselineFound || !session.EpochHeaderEqual(*baseline, headerSnapshot) {
		headerDraft, err := session.NewEventDraft(
			session.RequestHeaderSet,
			session.RequestHeaderSnapshot{
				Header: headerSnapshot,
				Reason: session.RequestHeaderChange,
			},
		)
		if err != nil {
			return requestAttempt{}, err
		}
		if _, err = executor.subject.conversation.Commit(requestContext, session.Batch(headerDraft)); err != nil {
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
	previousContext, err := session.LatestRequestContext(
		executor.subject.conversation.Events(),
	)
	if err != nil {
		return requestAttempt{}, err
	}
	contextFound := previousContext != nil
	if !contextFound || !sameRequestContext(*previousContext, routeContext) {
		contextDraft, err := session.NewEventDraft(
			session.RequestContextSet,
			routeContext,
		)
		if err != nil {
			return requestAttempt{}, err
		}
		if _, err := executor.subject.conversation.Commit(requestContext, session.Batch(contextDraft)); err != nil {
			return requestAttempt{}, err
		}
	}
	if err = contextFailure(requestContext); err != nil {
		return requestAttempt{}, err
	}
	requestOptions := llm.GenerateOptions{
		CallConfig: headerSnapshot.Config,
		Messages:   boundaryMessages,
		System:     headerSnapshot.System,
		Tools:      headerSnapshot.Tools,
		SessionID:  string(executor.subject.identifier),
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

func assistantToolCalls(reply agentmessage.AssistantMessage) []agentmessage.ToolCallBlock {
	blocks := reply.ContentValue()
	toolCalls := make([]agentmessage.ToolCallBlock, 0)
	for _, block := range blocks {
		switch retained := block.(type) {
		case agentmessage.ToolCallBlock:
			toolCalls = append(toolCalls, retained)
		case *agentmessage.ToolCallBlock:
			if retained != nil {
				toolCalls = append(toolCalls, *retained)
			}
		}
	}
	return toolCalls
}

type noRetryRequestErrorHandler struct{}

func (noRetryRequestErrorHandler) Execute(
	context.Context,
	agent.RequestErrorNotice,
) (agent.RequestErrorAction, error) {
	return agent.RequestErrorAction{}, nil
}

type requestConfigAction struct {
	config llm.CallConfig
}

func (action requestConfigAction) Execute(
	context.Context,
	agent.RequestNotice,
) (agent.RequestResolution, error) {
	return agent.RequestResolution{
		Config: llm.CloneCallConfig(action.config),
	}, nil
}
