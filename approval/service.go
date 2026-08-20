package approval

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"

	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/systemprompt"
)

const policyContextName = "approval:policy"

// Service owns approval policy, paired durable audit events, and the answerer
// Waterfall for one Runtime Scope.
type Service struct {
	plugin.Base
	name            string
	root            bool
	defaultPolicy   Policy
	parent          Approval
	prompts         systemprompt.PromptRegistry
	promptInstalled bool
}

// New constructs the root Approval Plugin from validated configuration.
func New(settings ValidatedConfig) *Service {
	return &Service{
		name:          PluginName,
		root:          true,
		defaultPolicy: settings.policy,
	}
}

// NewOverlay constructs a child Approval layer. It inherits policy from the
// nearest ancestor and starts answerer Waterfalls from its child Scope.
func NewOverlay() *Service {
	return &Service{
		name: OverlayPluginName,
	}
}

// Manifest declares Approval, System Prompt bindings, and the ancestor
// Approval required by an overlay.
func (owner *Service) Manifest() plugin.Manifest {
	requiredServices := []plugin.ServiceType{
		plugin.ServiceOf[systemprompt.PromptRegistry](),
	}
	if !owner.root {
		requiredServices = append(
			requiredServices,
			plugin.ServiceOf[Approval](),
		)
	}
	return plugin.Manifest{
		Name: owner.name,
		Provides: []plugin.ServiceType{
			plugin.ServiceOf[Approval](),
		},
		Requires: requiredServices,
	}
}

// Apply resolves dependencies and installs the policy context owned by this
// exact Approval layer.
func (owner *Service) Apply(requestContext context.Context) error {
	if err := requestContext.Err(); err != nil {
		return err
	}
	if !validPolicy(owner.defaultPolicy) && owner.root {
		return errors.New("approval: configuration was not validated")
	}
	if !owner.root {
		parent, err := plugin.Require[Approval](owner)
		if err != nil {
			return err
		}
		owner.parent = parent
	}
	prompts, err := plugin.Require[systemprompt.PromptRegistry](owner)
	if err != nil {
		return err
	}
	owner.prompts = prompts
	if err := prompts.AddContext(requestContext, systemprompt.PromptContext{
		Name:  policyContextName,
		Order: 115,
		Text: systemprompt.TextFunc(func(
			providerContext context.Context,
			assemblyContext systemprompt.AssembleContext,
		) (string, error) {
			if err := providerContext.Err(); err != nil {
				return "", err
			}
			if assemblyContext.Session == nil {
				return "", nil
			}
			selectedPolicy, policyErr := owner.EffectivePolicy(
				assemblyContext.Session,
			)
			if policyErr != nil {
				return "", policyErr
			}
			if selectedPolicy == PolicyNever {
				return neverPolicyText, nil
			}
			return askPolicyText, nil
		}),
	}); err != nil {
		return err
	}
	owner.promptInstalled = true
	return nil
}

// Dispose removes the prompt entry owned by this Plugin. Runtime has already
// hidden its Service binding.
func (owner *Service) Dispose(closeContext context.Context) error {
	var disposeErr error
	if owner.promptInstalled && owner.prompts != nil {
		disposeErr = owner.prompts.RemoveContext(closeContext, policyContextName)
	}
	owner.promptInstalled = false
	owner.prompts = nil
	owner.parent = nil
	return disposeErr
}

// Request writes the paired audit events around one policy/answerer decision.
func (owner *Service) Request(
	requestContext context.Context,
	decisionInput Request,
) (Outcome, error) {
	if requestContext == nil || decisionInput.Subject == nil ||
		decisionInput.Subject.SessionValue() == nil {
		return "", errors.New("approval: Context, Subject, and Session are required")
	}
	conversation := decisionInput.Subject.SessionValue()
	if !hasOpenTurn(conversation.Events()) {
		return "", errors.New(
			"approval: request outside an open turn; audit events must be turn-enclosed",
		)
	}
	identifier, err := mintRequestID()
	if err != nil {
		return "", fmt.Errorf("approval: mint request id: %w", err)
	}
	if identifier == "" {
		return "", errors.New("approval: minted request id is empty")
	}
	if _, err := session.AppendSerialized(conversation, AskedEvent, Asked{
		ID:       identifier,
		ToolName: decisionInput.ToolName,
		CallID:   cloneCallID(decisionInput.CallID),
		Reason:   cloneString(decisionInput.Reason),
	}); err != nil {
		return "", err
	}
	decisionOutcome := owner.decide(
		requestContext,
		decisionInput,
		conversation,
	)
	if _, err := session.AppendSerialized(conversation, DecidedEvent, Decided{
		ID:      identifier,
		Outcome: decisionOutcome,
	}); err != nil {
		return "", err
	}
	return decisionOutcome, nil
}

func (owner *Service) decide(
	requestContext context.Context,
	decisionInput Request,
	conversation *session.Session,
) Outcome {
	if requestContext.Err() != nil {
		return OutcomeCancelled
	}
	selectedPolicy, err := owner.EffectivePolicy(conversation)
	if err != nil {
		return OutcomeUnavailable
	}
	if selectedPolicy == PolicyNever {
		return OutcomeRejected
	}
	type result struct {
		outcome Outcome
		err     error
	}
	settled := make(chan result, 1)
	go func() {
		resolvedDecision, dispatchErr := plugin.Run(
			requestContext,
			owner,
			DecisionRequest{
				Request: decisionInput,
			},
			decisionTerminal{},
		)
		settled <- result{
			outcome: resolvedDecision.Outcome,
			err:     dispatchErr,
		}
	}()
	select {
	case <-requestContext.Done():
		return OutcomeCancelled
	case completed := <-settled:
		if completed.err != nil || !validOutcome(completed.outcome) {
			return OutcomeUnavailable
		}
		return completed.outcome
	}
}

type decisionTerminal struct{}

func (decisionTerminal) Execute(
	context.Context,
	DecisionRequest,
) (Decision, error) {
	return Decision{
		Outcome: OutcomeUnavailable,
	}, nil
}

func hasOpenTurn(entries []session.Event) bool {
	for index := len(entries) - 1; index >= 0; index-- {
		switch entries[index].Type {
		case session.TurnStartEventName:
			return true
		case session.TurnEndEventName:
			return false
		}
	}
	return false
}

func mintRequestID() (RequestID, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	raw[6] = (raw[6] & 0x0f) | 0x40
	raw[8] = (raw[8] & 0x3f) | 0x80
	return RequestID(fmt.Sprintf(
		"%08x-%04x-%04x-%04x-%012x",
		raw[0:4],
		raw[4:6],
		raw[6:8],
		raw[8:10],
		raw[10:16],
	)), nil
}
