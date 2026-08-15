package sessionapi

import (
	"context"
	"fmt"
	"slices"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/agentdefaultmodel"
	api "github.com/gorenx/goren/apiproxy"
	"github.com/gorenx/goren/connection"
	"github.com/gorenx/goren/llm"
)

type sessionModels struct {
	access    *sessionAccess
	runtime   llm.LlmRuntime
	directory *api.LLMGateway
	defaults  agentdefaultmodel.DefaultModel
}

func (selectionFlow *sessionModels) Models(requestContext context.Context, call api.Request[api.SessionModelsRequest]) (api.Outcome[api.SessionModelsValue], error) {
	subject, refused := selectionFlow.access.ordinaryAgent(requestContext, call.Payload.SessionID)
	if refused != nil {
		return api.Fail[api.SessionModelsValue](*refused), nil
	}
	selectionRef, err := selectionFlow.access.runtimeSessions.Selection(subject)
	if err != nil {
		return api.Outcome[api.SessionModelsValue]{}, err
	}
	current, _, err := selectionRef.Current()
	if err != nil {
		return api.Outcome[api.SessionModelsValue]{}, err
	}
	groups, failures := selectionFlow.directory.Catalog(requestContext)
	routable := slices.ContainsFunc(selectionFlow.runtime.ListProviders(), func(provider llm.ProviderInfo) bool {
		return provider.ID == current.Provider
	})
	return api.OK(api.SessionModelsValue{
		Current: modelSelectionValue(current), Routable: routable, Groups: groups, Failures: failures,
	}), nil
}

// SelectModel validates an exact route and applies it to the next assembled step.
func (selectionFlow *sessionModels) SelectModel(requestContext context.Context, call api.Request[api.SessionSelectModelRequest]) (api.Outcome[api.SessionSelectModelValue], error) {
	subject, refused := selectionFlow.access.ordinaryAgent(requestContext, call.Payload.SessionID)
	if refused != nil {
		return api.Fail[api.SessionSelectModelValue](*refused), nil
	}
	proposed := llm.CallConfig{Provider: call.Payload.Provider, Model: call.Payload.Model}
	if call.Payload.ReasoningEffort != nil {
		proposed.ReasoningEffort = llm.ReasoningEffortID(*call.Payload.ReasoningEffort)
	}
	resolved, err := selectionFlow.runtime.ResolveCallConfig(requestContext, proposed)
	if err != nil {
		return api.Fail[api.SessionSelectModelValue](api.NewRPCError(
			connection.ErrorModelUnavailable, err.Error(), struct {
				Provider string `json:"provider"`
				Model    string `json:"model"`
			}{Provider: call.Payload.Provider, Model: call.Payload.Model},
		)), nil
	}
	if agentContainsImage(subject) {
		metadata, resolveErr := selectionFlow.runtime.ResolveModelInfo(requestContext, resolved.Provider, resolved.Model)
		if resolveErr != nil {
			return api.Fail[api.SessionSelectModelValue](api.NewRPCError(
				connection.ErrorModelUnavailable, resolveErr.Error(), struct {
					Provider string `json:"provider"`
					Model    string `json:"model"`
				}{Provider: call.Payload.Provider, Model: call.Payload.Model},
			)), nil
		}
		if len(metadata.InputModalities) != 0 && !slices.Contains(metadata.InputModalities, llm.ModalityImage) {
			return api.Fail[api.SessionSelectModelValue](api.NewRPCError(
				connection.ErrorModelUnavailable,
				fmt.Sprintf("Model %q does not accept image input, but this session already contains images; select an image-capable model.", resolved.Model),
				struct {
					Provider string `json:"provider"`
					Model    string `json:"model"`
				}{Provider: call.Payload.Provider, Model: call.Payload.Model},
			)), nil
		}
	}
	selectionRef, err := selectionFlow.access.runtimeSessions.Selection(subject)
	if err != nil {
		return api.Outcome[api.SessionSelectModelValue]{}, err
	}
	selected := agent.ModelSelection{
		Provider: resolved.Provider, Model: resolved.Model, ReasoningEffort: resolved.ReasoningEffort,
	}
	selectionRef.SetCurrent(&selected)
	_ = selectionFlow.defaults.SaveSelection(requestContext, selected)
	return api.OK(api.SessionSelectModelValue{Selected: modelSelectionValue(selected)}), nil
}

func modelSelectionValue(selected agent.ModelSelection) api.ModelSelection {
	return api.ModelSelection{
		Provider: selected.Provider, Model: selected.Model,
		ReasoningEffort: string(selected.ReasoningEffort),
	}
}

func agentContainsImage(subject agent.Agent) bool {
	for _, messageValue := range append(subject.InboxValue().NextTurn(), subject.InboxValue().NextStep()...) {
		if llm.ContentHasImage(messageValue.ContentValue()) {
			return true
		}
	}
	messages, err := subject.SessionValue().DeriveMessages()
	if err != nil {
		return false
	}
	for _, messageValue := range messages {
		if llm.ContentHasImage(messageValue.ContentValue()) {
			return true
		}
	}
	return false
}
