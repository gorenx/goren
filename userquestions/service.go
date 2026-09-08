package userquestions

import (
	"context"
	"errors"
	"fmt"
)

// QuestionService validates and forwards questions. Dependencies are immutable;
// each invocation owns its input and answer, so concurrent asks share no state.
type QuestionService struct {
	provider Provider
	callers  Callers
}

// New constructs the question capability. A nil provider returns CodeNoProvider
// when asked; an Agent-backed request requires a caller authority.
func New(answerer Provider, authority Callers) *QuestionService {
	return &QuestionService{
		provider: answerer,
		callers:  authority,
	}
}

// Ask validates caller eligibility and presentation before invoking the provider.
func (owner *QuestionService) Ask(ctx context.Context, input Request) (Answer, error) {
	if ctx == nil {
		return Answer{}, errors.New("userquestions: context is required")
	}
	if ctx.Err() != nil {
		return Answer{}, newError("ask_user_question was aborted before the user answered", CodeAborted, context.Cause(ctx))
	}
	if len(input.Questions) == 0 {
		return Answer{}, newError("ask_user_question requires at least one question", CodeEmptyQuestions)
	}
	if input.SessionID != "" {
		if owner.callers == nil {
			return Answer{}, newError("human interaction requires a live calling agent", CodeCallerNotLive)
		}
		eligibility, err := owner.callers.CheckCaller(ctx, input.SessionID)
		if err != nil {
			return Answer{}, err
		}
		if !eligibility.Live {
			return Answer{}, newError("human interaction requires a live calling agent", CodeCallerNotLive)
		}
		if eligibility.Delegated {
			return Answer{}, newError("human interaction is unavailable while the calling agent is owned by another live agent; include the unresolved question or decision in the child agent's final result", CodeDelegatedCaller)
		}
	}
	if err := validateIntents(input.Questions); err != nil {
		return Answer{}, err
	}
	if owner.provider == nil {
		return Answer{}, newError("no user-questions provider is registered", CodeNoProvider)
	}
	input.Questions = cloneQuestions(input.Questions)
	answerValue, err := owner.provider.Ask(ctx, input)
	if ctx.Err() != nil {
		return Answer{}, newError("ask_user_question was aborted before the user answered", CodeAborted, context.Cause(ctx))
	}
	if err != nil {
		return Answer{}, err
	}
	return cloneAnswer(answerValue), nil
}

func validateIntents(questions []Question) error {
	for _, item := range questions {
		if item.Intent == nil {
			continue
		}
		if item.Intent.Kind != IntentPlanReview {
			return newError(fmt.Sprintf("question %s declares an unknown intent", item.ID), CodeBadIntent)
		}
		matched := false
		if item.Options != nil {
			for _, offeredChoice := range *item.Options {
				if offeredChoice.Label == item.Intent.Approve {
					matched = true
					break
				}
			}
		}
		if !matched {
			return newError(fmt.Sprintf(
				"question %s declares intent %s whose approve label %q names none of its options",
				item.ID, item.Intent.Kind, item.Intent.Approve,
			), CodeBadIntent)
		}
		if item.Detail == nil {
			return newError(fmt.Sprintf(
				"question %s declares intent %s without the detail it reviews", item.ID, item.Intent.Kind,
			), CodeBadIntent)
		}
	}
	return nil
}

var _ UserQuestions = (*QuestionService)(nil)
