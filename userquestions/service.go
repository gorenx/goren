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

// QuestionService owns the single active UI Provider and validates that an
// Agent-backed ask originates from the exact live root Agent.
type QuestionService struct {
	plugin.Base

	mu       sync.RWMutex
	active   bool
	agents   agent.Registry
	provider *providerSlot
}

// New constructs an inactive User Questions Plugin.
func New() *QuestionService {
	return &QuestionService{}
}

// Manifest provides UserQuestions and optionally consumes the Agent Registry
// used to attest Agent-backed requests.
func (owner *QuestionService) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: PluginName,
		Provides: []plugin.ProvidedService{
			plugin.NewProvidedService[UserQuestions](owner),
		},
		Optional: []plugin.ServiceType{
			plugin.ServiceOf[agent.Registry](),
		},
	}
}

// Apply captures the optional Registry dependency before Service publication.
func (owner *QuestionService) Apply(requestContext context.Context) error {
	if err := requestContext.Err(); err != nil {
		return err
	}
	agents, found := plugin.Resolve[agent.Registry](owner)
	owner.mu.Lock()
	owner.active = true
	if found {
		owner.agents = agents
	} else {
		owner.agents = nil
	}
	owner.mu.Unlock()
	return nil
}

// Dispose withdraws the active Provider after dependents have stopped.
func (owner *QuestionService) Dispose(context.Context) error {
	owner.mu.Lock()
	owner.active = false
	owner.agents = nil
	owner.provider = nil
	owner.mu.Unlock()
	return nil
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
	if !owner.active {
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
