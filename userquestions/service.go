package userquestions

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/session"
)

type providerSlot struct {
	target Provider
}

// ProviderHandle owns one exact UI Provider registration. Unregister is
// idempotent and cannot remove a later Provider.
type ProviderHandle struct {
	once  sync.Once
	owner *QuestionService
	slot  *providerSlot
}

// Unregister releases the exact Provider registration represented by this
// handle.
func (binding *ProviderHandle) Unregister() {
	if binding == nil {
		return
	}
	binding.once.Do(func() {
		binding.owner.removeProvider(binding.slot)
	})
}

type questionServiceState uint8

const (
	// questionServiceInactive accepts no Provider registration. A later Plugin
	// activation may bind the Service to the current optional Agent Registry.
	questionServiceInactive questionServiceState = iota
	// questionServiceActive accepts one Provider and serves question requests.
	questionServiceActive
)

// QuestionService owns the single active UI Provider and validates that an
// Agent-backed ask originates from the exact live root Agent. Plugin lifecycle
// adaptation is owned separately by Plugin.
type QuestionService struct {
	mu       sync.RWMutex
	state    questionServiceState
	agents   agent.Registry
	provider *providerSlot
}

func newQuestionService() *QuestionService {
	return &QuestionService{}
}

func (owner *QuestionService) activate(agents agent.Registry) error {
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if owner.state != questionServiceInactive {
		return errors.New("userquestions: Question Service cannot be activated again")
	}
	owner.state = questionServiceActive
	owner.agents = agents
	return nil
}

func (owner *QuestionService) deactivate() {
	owner.mu.Lock()
	owner.state = questionServiceInactive
	owner.agents = nil
	owner.provider = nil
	owner.mu.Unlock()
}

// RegisterProvider installs the one active UI Provider.
func (owner *QuestionService) RegisterProvider(
	answerProvider Provider,
) (*ProviderHandle, error) {
	if answerProvider == nil {
		return nil, errors.New("userquestions: Provider is required")
	}
	slot := &providerSlot{target: answerProvider}
	owner.mu.Lock()
	if owner.state != questionServiceActive {
		owner.mu.Unlock()
		return nil, errors.New("userquestions: service is not active")
	}
	if owner.provider != nil {
		owner.mu.Unlock()
		return nil, newError("a user-questions provider is already registered", CodeDuplicate)
	}
	owner.provider = slot
	owner.mu.Unlock()
	return &ProviderHandle{
		owner: owner,
		slot:  slot,
	}, nil
}

func (owner *QuestionService) removeProvider(slot *providerSlot) {
	owner.mu.Lock()
	if owner.provider == slot {
		owner.provider = nil
	}
	owner.mu.Unlock()
}

func (owner *QuestionService) Ask(requestContext context.Context, questionRequest Request) (Answer, error) {
	if requestContext == nil {
		return Answer{}, errors.New("userquestions: Context is nil")
	}
	if requestContext.Err() != nil {
		return Answer{}, newError("ask_user_question was aborted before the user answered", CodeAborted, requestContext.Err())
	}
	if len(questionRequest.Questions) == 0 {
		return Answer{}, newError("ask_user_question requires at least one question", CodeEmptyQuestions)
	}
	if err := owner.validateSubject(questionRequest.Subject); err != nil {
		return Answer{}, err
	}
	if err := validateIntents(questionRequest.Questions); err != nil {
		return Answer{}, err
	}
	owner.mu.RLock()
	slot := owner.provider
	owner.mu.RUnlock()
	if slot == nil {
		return Answer{}, newError("no user-questions provider is registered", CodeNoProvider)
	}
	detachedRequest := Request{
		Questions: cloneQuestions(questionRequest.Questions),
		Subject:   questionRequest.Subject,
	}
	answerValue, err := slot.target.Ask(requestContext, detachedRequest)
	if err != nil {
		return Answer{}, err
	}
	return cloneAnswer(answerValue), nil
}

func (owner *QuestionService) validateSubject(agentSubject agent.Agent) error {
	if agentSubject == nil {
		return nil
	}
	owner.mu.RLock()
	agentRegistry := owner.agents
	owner.mu.RUnlock()
	if agentRegistry == nil {
		return newError(
			"human interaction requires the exact live calling agent when an agent is supplied",
			CodeCallerNotLive,
		)
	}
	liveSubject, found := agentRegistry.Get(agentSubject.ID())
	if !found || !sameAgent(liveSubject, agentSubject) {
		return newError(
			"human interaction requires the exact live calling agent when an agent is supplied",
			CodeCallerNotLive,
		)
	}
	if agentSubject.SessionValue().Header().Origin != session.OriginSubagent {
		return nil
	}
	return newError(
		"human interaction is unavailable while the calling agent is owned by another live agent; "+
			"include the unresolved question or decision in the child agent's final result",
		CodeDelegatedCaller,
	)
}

func sameAgent(leftSubject agent.Agent, rightSubject agent.Agent) bool {
	return agent.Same(leftSubject, rightSubject)
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
