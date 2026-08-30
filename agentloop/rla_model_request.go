package agentloop

import (
	"context"
	"errors"
	"fmt"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/agentloop/internal/modelrequest"
	stepstate "github.com/gorenx/goren/agentloop/internal/step"
	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/systemprompt"
)

// modelCall is one adapter-ready LLM call derived from the current durable
// Step boundary.
type modelCall struct {
	options  llm.GenerateOptions
	prepared llm.PreparedLlmCall
}

// executeModelRequest owns one ModelRequest, including explicit retry
// transitions, Assistant assembly, and optional ToolBatch entry.
func (subject *ReactLoopAgent) executeModelRequest(
	requestContext context.Context,
	currentStep *stepstate.Step,
	proposal *stepProposal,
) (stepstate.StepResult, error) {
	if currentStep == nil || proposal == nil {
		return stepstate.StepResultError, errors.New(
			"agentloop: ModelRequest requires an active Step",
		)
	}
	turnNumber, stepNumber := currentStep.Position()
	systemText, err := systemprompt.RenderPrompt(proposal.assembly)
	if err != nil {
		return stepstate.StepResultError, err
	}
	requestState := modelrequest.New()
	for {
		if err = contextFailure(requestContext); err != nil {
			_ = requestState.EnterAborted()
			return stepstate.StepResultAborted, err
		}
		boundaryMessages, deriveErr := subject.conversation.DeriveMessages()
		if deriveErr != nil {
			_ = requestState.EnterFailed()
			return stepstate.StepResultError, deriveErr
		}
		call, buildErr := subject.buildModelCall(
			requestContext,
			turnNumber,
			stepNumber,
			proposal.assembly.Tools,
			systemText,
			boundaryMessages,
		)
		if buildErr != nil {
			if requestContext.Err() != nil {
				_ = requestState.EnterAborted()
				return stepstate.StepResultAborted, buildErr
			}
			_ = requestState.EnterFailed()
			return stepstate.StepResultError, buildErr
		}
		if _, err = requestState.EnterStreaming(); err != nil {
			return stepstate.StepResultError, err
		}
		assembler := llm.NewBlockAssembler()
		chunkSequences := make([]int64, 0)
		var chunkStream llm.ChunkStream
		if call.prepared != nil {
			chunkStream, err = call.prepared.Stream(requestContext, call.options)
		} else {
			chunkStream, err = subject.models.Stream(requestContext, call.options)
		}
		if err != nil {
			_ = requestState.EnterFailed()
			return stepstate.StepResultError, err
		}
		for {
			if err = contextFailure(requestContext); err != nil {
				_ = chunkStream.Close(context.Background())
				_ = requestState.EnterAborted()
				return stepstate.StepResultAborted, err
			}
			chunk, found, nextErr := chunkStream.Next(requestContext)
			if nextErr != nil {
				_ = chunkStream.Close(context.Background())
				_ = requestState.EnterFailed()
				return stepstate.StepResultError, nextErr
			}
			if !found {
				break
			}
			chunkDraft, appendErr := session.NewEventDraft(
				session.AssistantChunked,
				session.AssistantChunk{
					Turn:  turnNumber,
					Step:  stepNumber,
					Chunk: chunk,
				},
			)
			if appendErr != nil {
				_ = chunkStream.Close(context.Background())
				_ = requestState.EnterFailed()
				return stepstate.StepResultError, appendErr
			}
			commitResult, appendErr := subject.conversation.Commit(
				requestContext,
				session.Batch(chunkDraft),
			)
			if appendErr != nil {
				_ = chunkStream.Close(context.Background())
				_ = requestState.EnterFailed()
				return stepstate.StepResultError, appendErr
			}
			chunkSequences = append(
				chunkSequences,
				commitResult.Events[0].Seq,
			)
			if err = assembler.Push(chunk); err != nil {
				_ = chunkStream.Close(context.Background())
				_ = requestState.EnterFailed()
				return stepstate.StepResultError, err
			}
		}
		if err = chunkStream.Close(context.Background()); err != nil {
			_ = requestState.EnterFailed()
			return stepstate.StepResultError, err
		}
		if err = contextFailure(requestContext); err != nil {
			_ = requestState.EnterAborted()
			return stepstate.StepResultAborted, err
		}
		finishReason := assembler.FinishValue()
		if failure, failed := finishFailure(finishReason); failed {
			var retryPolicy llm.RetryPolicy
			if call.prepared != nil {
				retryPolicy = call.prepared.RetryPolicyValue()
			}
			action, resolveErr := subject.waterfalls.ResolveRequestError(
				requestContext,
				agent.RequestErrorNotice{
					Subject:     subject,
					Turn:        turnNumber,
					Step:        stepNumber,
					Provider:    call.options.Provider,
					Failure:     failure,
					RetryPolicy: retryPolicy,
				},
				noRetryRequestErrorHandler{},
			)
			if resolveErr != nil {
				_ = requestState.EnterFailed()
				return stepstate.StepResultError, resolveErr
			}
			if err = contextFailure(requestContext); err != nil {
				_ = requestState.EnterAborted()
				return stepstate.StepResultAborted, err
			}
			if action.Retry {
				if err = requestState.EnterRetryPending(); err != nil {
					return stepstate.StepResultError, err
				}
				continue
			}
			_ = requestState.EnterFailed()
			return stepstate.StepResultError, llmErrorFromFailure(failure)
		}

		blocks, assembleErr := assembler.AssembledBlocks()
		if assembleErr != nil {
			_ = requestState.EnterFailed()
			return stepstate.StepResultError, assembleErr
		}
		assistantReply, messageErr := agentmessage.NewAssistantMessage(
			agentmessage.AssistantMessageInput{
				Content: blocks,
				Source: agentmessage.ModelMessageSource{
					Provider:    call.options.Provider,
					Model:       call.options.Model,
					ReplayState: assembler.ReplayValue(),
				},
			},
		)
		if messageErr != nil {
			_ = requestState.EnterFailed()
			return stepstate.StepResultError, messageErr
		}
		assembledPayload := session.AssistantMessage{
			Turn:    turnNumber,
			Step:    stepNumber,
			Message: assistantReply,
		}
		if usage, present := assembler.UsageValue(); present {
			assembledPayload.Usage = &usage
		}
		messageDraft, messageErr := session.NewSurfaceEventDraft(
			session.AssistantMessaged,
			assembledPayload,
			session.SurfaceIntent{
				Operation:       session.SurfaceAppend(),
				SourceEventSeqs: &chunkSequences,
			},
		)
		if messageErr == nil {
			_, messageErr = subject.conversation.Commit(
				requestContext,
				session.Batch(messageDraft),
			)
		}
		if messageErr != nil {
			_ = requestState.EnterFailed()
			return stepstate.StepResultError, messageErr
		}
		if err = requestState.EnterAccepted(); err != nil {
			return stepstate.StepResultError, err
		}
		if finishReason.ReasonKind() == "max-tokens" {
			return stepstate.StepResultMaxTokens, nil
		}
		toolCalls := assistantToolCalls(assistantReply)
		if len(toolCalls) == 0 {
			return stepstate.StepResultCompleted, nil
		}
		if err = currentStep.EnterTooling(); err != nil {
			return stepstate.StepResultError, err
		}
		stopsContinuation, toolErr := subject.executeToolBatch(
			requestContext,
			turnNumber,
			stepNumber,
			toolCalls,
		)
		if toolErr != nil {
			if requestContext.Err() != nil {
				return stepstate.StepResultAborted, toolErr
			}
			return stepstate.StepResultError, toolErr
		}
		if stopsContinuation {
			return stepstate.StepResultCompleted, nil
		}
		return stepstate.StepResultContinue, nil
	}
}

func (subject *ReactLoopAgent) buildModelCall(
	requestContext context.Context,
	turnNumber int64,
	stepNumber int64,
	toolSchemas []llm.ToolSchema,
	systemText string,
	boundaryMessages []agentmessage.Message,
) (modelCall, error) {
	persistedHeader, err := session.LatestRequestHeader(
		subject.conversation.Events(),
	)
	if err != nil {
		return modelCall{}, err
	}
	headerFound := persistedHeader != nil
	agentOptions := subject.OptionsValue()
	seedConfig := llm.CallConfig{
		Provider:  agentOptions.Provider,
		Model:     agentOptions.Model,
		MaxTokens: cloneInt(agentOptions.MaxTokens),
	}
	if headerFound && persistedHeader.Config.Provider == seedConfig.Provider &&
		persistedHeader.Config.Model == seedConfig.Model &&
		(persistedHeader.AdapterDefaults == nil ||
			!persistedHeader.AdapterDefaults.ReasoningEffort) {
		seedConfig.ReasoningEffort = persistedHeader.Config.ReasoningEffort
	}
	if subject.requestHeaderLogged && headerFound {
		seedConfig = requestProposal(*persistedHeader)
	}
	resolution, err := subject.waterfalls.ResolveRequest(
		requestContext,
		agent.RequestNotice{
			Subject: subject,
			Turn:    turnNumber,
			Step:    stepNumber,
		},
		requestConfigAction{
			config: llm.CloneCallConfig(seedConfig),
		},
	)
	if err != nil {
		return modelCall{}, err
	}
	if err = contextFailure(requestContext); err != nil {
		return modelCall{}, err
	}
	proposed := resolution.Config
	if proposed.Provider == "" || proposed.Model == "" {
		return modelCall{}, fmt.Errorf(
			"agentloop: Agent %q has no provider/model; set Agent Options or supply both through agent/request",
			subject.identifier,
		)
	}
	preparedCall, err := subject.models.PrepareCall(requestContext, proposed)
	effective := proposed
	if err != nil {
		var llmProblem *llm.LlmError
		if !errors.As(err, &llmProblem) || llmProblem.Code() != "NO_ADAPTER" {
			return modelCall{}, err
		}
		preparedCall = nil
	} else {
		effective = preparedCall.ConfigValue()
	}
	if err = contextFailure(requestContext); err != nil {
		return modelCall{}, err
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
	baseline, err := session.LatestRequestHeader(subject.conversation.Events())
	if err != nil {
		return modelCall{}, err
	}
	baselineFound := baseline != nil
	if !subject.requestHeaderLogged {
		reason := session.RequestHeaderInitial
		if baselineFound {
			reason = session.RequestHeaderResume
		}
		headerDraft, draftErr := session.NewEventDraft(
			session.RequestHeaderSet,
			session.RequestHeaderSnapshot{
				Header: headerSnapshot,
				Reason: reason,
			},
		)
		if draftErr == nil {
			_, draftErr = subject.conversation.Commit(
				requestContext,
				session.Batch(headerDraft),
			)
		}
		if draftErr != nil {
			return modelCall{}, draftErr
		}
		subject.requestHeaderLogged = true
	} else if !baselineFound ||
		!session.EpochHeaderEqual(*baseline, headerSnapshot) {
		headerDraft, draftErr := session.NewEventDraft(
			session.RequestHeaderSet,
			session.RequestHeaderSnapshot{
				Header: headerSnapshot,
				Reason: session.RequestHeaderChange,
			},
		)
		if draftErr == nil {
			_, draftErr = subject.conversation.Commit(
				requestContext,
				session.Batch(headerDraft),
			)
		}
		if draftErr != nil {
			return modelCall{}, draftErr
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
		subject.conversation.Events(),
	)
	if err != nil {
		return modelCall{}, err
	}
	if previousContext == nil ||
		!sameRequestContext(*previousContext, routeContext) {
		contextDraft, draftErr := session.NewEventDraft(
			session.RequestContextSet,
			routeContext,
		)
		if draftErr == nil {
			_, draftErr = subject.conversation.Commit(
				requestContext,
				session.Batch(contextDraft),
			)
		}
		if draftErr != nil {
			return modelCall{}, draftErr
		}
	}
	if err = contextFailure(requestContext); err != nil {
		return modelCall{}, err
	}
	requestOptions := llm.GenerateOptions{
		CallConfig: headerSnapshot.Config,
		Messages:   boundaryMessages,
		System:     headerSnapshot.System,
		Tools:      headerSnapshot.Tools,
		SessionID:  string(subject.identifier),
	}
	detached, err := llm.CloneGenerateOptions(requestOptions)
	if err != nil {
		return modelCall{}, err
	}
	return modelCall{
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

func sameRequestContext(
	left session.RequestRouteContext,
	right session.RequestRouteContext,
) bool {
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
	problem, err := llm.NewLlmError(
		failure.Message,
		failure.Code,
		llm.LlmErrorOptions{
			Status:               failure.Status,
			ProviderRetryAfterMS: failure.ProviderRetryAfterMS,
			RequestID:            failure.RequestID,
		},
	)
	if err != nil {
		return fmt.Errorf("agentloop: invalid LLM failure: %w", err)
	}
	return problem
}

func assistantToolCalls(
	reply agentmessage.AssistantMessage,
) []agentmessage.ToolCallBlock {
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
