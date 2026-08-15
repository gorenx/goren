package userquestions

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/plugin"
)

type providerSlot struct {
	target Provider
}

type questionService struct {
	agents AgentRegistryResolver

	mu       sync.RWMutex
	provider *providerSlot
}

// New creates one User Questions service with a live optional Agent-registry lookup.
func New(agentResolver AgentRegistryResolver) UserQuestions {
	return &questionService{agents: agentResolver}
}

func (owner *questionService) RegisterProvider(
	requestContext context.Context,
	ownerScope *plugin.Scope,
	answerProvider Provider,
) (plugin.Disposer, error) {
	if requestContext == nil || ownerScope == nil || answerProvider == nil {
		return nil, errors.New("userquestions: Context, provider Scope, and Provider are required")
	}
	slot := &providerSlot{target: answerProvider}
	owner.mu.Lock()
	if owner.provider != nil {
		owner.mu.Unlock()
		return nil, newError("a user-questions provider is already registered", CodeDuplicate)
	}
	owner.provider = slot
	owner.mu.Unlock()
	release, err := plugin.Own(ownerScope, "userInteraction.registerProvider()", func(context.Context) error {
		owner.mu.Lock()
		if owner.provider == slot {
			owner.provider = nil
		}
		owner.mu.Unlock()
		return nil
	})
	if err != nil {
		owner.mu.Lock()
		if owner.provider == slot {
			owner.provider = nil
		}
		owner.mu.Unlock()
		return nil, err
	}
	if err := requestContext.Err(); err != nil {
		return nil, errors.Join(err, release(context.Background()))
	}
	return release, nil
}

func (owner *questionService) Ask(requestContext context.Context, questionRequest Request) (Answer, error) {
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
	detachedRequest := Request{Questions: cloneQuestions(questionRequest.Questions), Subject: questionRequest.Subject}
	answerValue, err := slot.target.Ask(requestContext, detachedRequest)
	if err != nil {
		return Answer{}, err
	}
	return cloneAnswer(answerValue), nil
}

func (owner *questionService) validateSubject(agentSubject agent.Agent) error {
	if agentSubject == nil {
		return nil
	}
	if owner.agents == nil {
		return newError(
			"human interaction requires the exact live calling agent when an agent is supplied",
			CodeCallerNotLive,
		)
	}
	agentRegistry, found := owner.agents.ResolveAgentRegistry()
	if !found || agentRegistry == nil {
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
	for _, rootSubject := range agentRegistry.Roots() {
		if sameAgent(rootSubject, agentSubject) {
			return nil
		}
	}
	return newError(
		"human interaction is unavailable while the calling agent is owned by another live agent; "+
			"include the unresolved question or decision in the child agent's final result",
		CodeDelegatedCaller,
	)
}

func sameAgent(leftSubject agent.Agent, rightSubject agent.Agent) bool {
	return leftSubject != nil && rightSubject != nil &&
		leftSubject.ID() == rightSubject.ID() &&
		leftSubject.SessionValue() == rightSubject.SessionValue() &&
		leftSubject.ScopeValue() == rightSubject.ScopeValue()
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
