package apiproxy

import (
	"context"
	"errors"
	"fmt"

	"github.com/gorenx/goren/connection"
	"github.com/gorenx/goren/userquestions"
)

type questionClientResolution struct {
	answerValue userquestions.Answer
	cancelled   bool
}

type pendingQuestion struct {
	rpcID      connection.RPCID
	sessionID  SessionID
	items      []AskUserQuestionItem
	waiting    *PendingResponse[questionClientResolution]
	settlement interactionSettlement
}

// Ask implements the UserQuestions provider seam with one stable server
// request that remains answerable across mux reconnects.
func (owner *InteractionGateway) Ask(
	requestContext context.Context,
	questionRequest userquestions.Request,
) (userquestions.Answer, error) {
	if requestContext == nil {
		return userquestions.Answer{}, errors.New("apiproxy: question Context is nil")
	}
	if questionRequest.Subject == nil {
		return userquestions.Answer{}, questionFailure(
			"web user interaction requires an agent-owned session",
			userquestions.CodeMissingAgent,
			nil,
		)
	}
	if requestContext.Err() != nil {
		return userquestions.Answer{}, questionFailure(
			"ask_user_question was aborted before the user answered",
			userquestions.CodeAborted,
			requestContext.Err(),
		)
	}
	items := projectQuestions(questionRequest.Questions)
	conversationID := SessionID(questionRequest.Subject.ID())
	correlationID, err := owner.newRPC()
	if err != nil {
		return userquestions.Answer{}, fmt.Errorf("apiproxy: mint question rpcId: %w", err)
	}
	waiting, err := RegisterPendingResponse(owner.methods, correlationID,
		func(result connection.RPCResult) (questionClientResolution, bool) {
			return decodeQuestionResponse(result, conversationID, items)
		})
	if err != nil {
		return userquestions.Answer{}, err
	}
	entry := &pendingQuestion{
		rpcID: correlationID, sessionID: conversationID, items: items, waiting: waiting,
		settlement: newInteractionSettlement(),
	}

	owner.mu.Lock()
	if owner.closed {
		owner.mu.Unlock()
		waiting.Withdraw(errInteractionGatewayClosed)
		entry.settlement.complete(nil)
		return userquestions.Answer{}, questionFailure(
			"web user-questions provider was disposed",
			userquestions.CodeAborted,
			errInteractionGatewayClosed,
		)
	}
	if requestContext.Err() != nil {
		owner.mu.Unlock()
		waiting.Withdraw(requestContext.Err())
		entry.settlement.complete(nil)
		return userquestions.Answer{}, questionFailure(
			"ask_user_question was aborted before the user answered",
			userquestions.CodeAborted,
			requestContext.Err(),
		)
	}
	owner.questions[correlationID] = entry
	publishErr := owner.frames.PublishPending(correlationID, QuestionRequestedFrame{
		SessionID: conversationID, Questions: cloneQuestionItems(items),
	})
	if publishErr != nil {
		delete(owner.questions, correlationID)
		waiting.Withdraw(publishErr)
		entry.settlement.complete(nil)
		owner.mu.Unlock()
		return userquestions.Answer{}, publishErr
	}
	owner.mu.Unlock()

	clientResolution, waitErr := waiting.Wait(requestContext)
	if waitErr != nil {
		entry.finish(owner, QuestionCancelled)
		switch {
		case errors.Is(waitErr, errInteractionGatewayClosed):
			return userquestions.Answer{}, questionFailure(
				"web user-questions provider was disposed",
				userquestions.CodeAborted,
				waitErr,
			)
		case errors.Is(waitErr, context.Canceled), errors.Is(waitErr, context.DeadlineExceeded):
			return userquestions.Answer{}, questionFailure(
				"ask_user_question was aborted before the user answered",
				userquestions.CodeAborted,
				waitErr,
			)
		default:
			return userquestions.Answer{}, waitErr
		}
	}
	if clientResolution.cancelled {
		entry.finish(owner, QuestionCancelled)
		return userquestions.Answer{}, questionFailure(
			"the user cancelled ask_user_question",
			userquestions.CodeCancelled,
			nil,
		)
	}
	entry.finish(owner, QuestionAnswered)
	return clientResolution.answerValue, nil
}

func (entry *pendingQuestion) finish(owner *InteractionGateway, resolution QuestionResolution) {
	entry.settlement.complete(func() {
		owner.mu.Lock()
		if owner.questions[entry.rpcID] == entry {
			delete(owner.questions, entry.rpcID)
		}
		owner.mu.Unlock()
		owner.report(owner.frames.ResolvePending(entry.rpcID, QuestionResolvedFrame{
			SessionID: entry.sessionID, QuestionRPCID: entry.rpcID, Outcome: resolution,
		}))
	})
}

func projectQuestions(source []userquestions.Question) []AskUserQuestionItem {
	items := make([]AskUserQuestionItem, len(source))
	for index, questionValue := range source {
		items[index] = AskUserQuestionItem{
			ID: questionValue.ID, Question: questionValue.Question,
			Header:      cloneInteractionString(questionValue.Header),
			Detail:      cloneInteractionString(questionValue.Detail),
			MultiSelect: cloneInteractionBool(questionValue.MultiSelect),
		}
		if questionValue.Options != nil {
			options := make([]QuestionOption, len(*questionValue.Options))
			for optionIndex, optionValue := range *questionValue.Options {
				options[optionIndex] = QuestionOption{
					Label: optionValue.Label, Description: cloneInteractionString(optionValue.Description),
				}
			}
			items[index].Options = &options
		}
		if questionValue.Intent != nil {
			items[index].Intent = &QuestionIntent{
				Kind: string(questionValue.Intent.Kind), Approve: questionValue.Intent.Approve,
			}
		}
	}
	return items
}

func cloneQuestionItems(source []AskUserQuestionItem) []AskUserQuestionItem {
	items := make([]AskUserQuestionItem, len(source))
	for index, questionValue := range source {
		items[index] = questionValue
		items[index].Header = cloneInteractionString(questionValue.Header)
		items[index].Detail = cloneInteractionString(questionValue.Detail)
		items[index].MultiSelect = cloneInteractionBool(questionValue.MultiSelect)
		if questionValue.Options != nil {
			options := make([]QuestionOption, len(*questionValue.Options))
			for optionIndex, optionValue := range *questionValue.Options {
				options[optionIndex] = optionValue
				options[optionIndex].Description = cloneInteractionString(optionValue.Description)
			}
			items[index].Options = &options
		}
		if questionValue.Intent != nil {
			intentValue := *questionValue.Intent
			items[index].Intent = &intentValue
		}
	}
	return items
}

func cloneInteractionBool(source *bool) *bool {
	if source == nil {
		return nil
	}
	snapshot := *source
	return &snapshot
}

func questionFailure(message string, code string, cause error) *userquestions.Error {
	return &userquestions.Error{Message: message, Code: code, Cause: cause}
}
