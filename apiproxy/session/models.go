package sessionapi

import (
	"context"
	"fmt"
	"slices"

	llmapi "github.com/gorenx/goren/apiproxy/llm"
	rpc "github.com/gorenx/goren/apiproxy/rpc"

	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/connection"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/session"
)

// Models is the Session API's directory and exact selection validation boundary.
// It does not expose model generation, registration or LLM resource ownership.
type Models interface {
	llmapi.Directory
	Configuration(context.Context, session.SessionID) (llm.CallConfig, error)
	Configure(context.Context, session.SessionID, llm.CallConfig) error
	ResolveCallConfig(context.Context, llm.CallConfig) (llm.CallConfig, error)
}

type sessionModels struct {
	access    *sessionAccess
	models    Models
	directory *llmapi.Catalog
}

func (selectionFlow *sessionModels) Models(requestContext context.Context, call rpc.Request[ModelsRequest]) (rpc.Outcome[ModelsValue], error) {
	subject, refused := selectionFlow.access.ordinaryAgent(requestContext, call.Payload.SessionID)
	if refused != nil {
		return rpc.Fail[ModelsValue](*refused), nil
	}
	current, err := selectionFlow.models.Configuration(requestContext, subject.ID())
	if err != nil {
		return rpc.Outcome[ModelsValue]{}, err
	}
	groups, failures, err := selectionFlow.directory.List(requestContext)
	if err != nil {
		return rpc.Outcome[ModelsValue]{}, err
	}
	catalog, err := selectionFlow.models.ReadCatalog(requestContext)
	if err != nil {
		return rpc.Outcome[ModelsValue]{}, err
	}
	routable := slices.ContainsFunc(catalog.Active, func(provider llm.ProviderInfo) bool {
		return provider.ID == current.Provider
	})
	return rpc.OK(ModelsValue{
		Current:  modelSelectionValue(current),
		Routable: routable,
		Groups:   groups,
		Failures: failures,
	}), nil
}

// SelectModel validates an exact route and applies it to the next assembled step.
func (selectionFlow *sessionModels) SelectModel(requestContext context.Context, call rpc.Request[SelectModelRequest]) (rpc.Outcome[SelectModelValue], error) {
	subject, refused := selectionFlow.access.ordinaryAgent(requestContext, call.Payload.SessionID)
	if refused != nil {
		return rpc.Fail[SelectModelValue](*refused), nil
	}
	proposed := llm.CallConfig{
		Provider: call.Payload.Provider,
		Model:    call.Payload.Model,
	}
	if call.Payload.ReasoningEffort != nil {
		proposed.ReasoningEffort = llm.ReasoningEffortID(*call.Payload.ReasoningEffort)
	}
	resolved, err := selectionFlow.models.ResolveCallConfig(requestContext, proposed)
	if err != nil {
		return rpc.Fail[SelectModelValue](rpc.NewRPCError(
			connection.ErrorModelUnavailable, err.Error(), struct {
				Provider string `json:"provider"`
				Model    string `json:"model"`
			}{
				Provider: call.Payload.Provider,
				Model:    call.Payload.Model,
			},
		)), nil
	}
	hasImage, err := agentContainsImage(requestContext, subject)
	if err != nil {
		return rpc.Outcome[SelectModelValue]{}, err
	}
	if hasImage {
		metadata, resolveErr := selectionFlow.models.ResolveModelInfo(requestContext, resolved.Provider, resolved.Model)
		if resolveErr != nil {
			return rpc.Fail[SelectModelValue](rpc.NewRPCError(
				connection.ErrorModelUnavailable, resolveErr.Error(), struct {
					Provider string `json:"provider"`
					Model    string `json:"model"`
				}{
					Provider: call.Payload.Provider,
					Model:    call.Payload.Model,
				},
			)), nil
		}
		if len(metadata.InputModalities) != 0 && !slices.Contains(metadata.InputModalities, llm.ModalityImage) {
			return rpc.Fail[SelectModelValue](rpc.NewRPCError(
				connection.ErrorModelUnavailable,
				fmt.Sprintf("Model %q does not accept image input, but this session already contains images; select an image-capable model.", resolved.Model),
				struct {
					Provider string `json:"provider"`
					Model    string `json:"model"`
				}{
					Provider: call.Payload.Provider,
					Model:    call.Payload.Model,
				},
			)), nil
		}
	}
	if err := selectionFlow.models.Configure(requestContext, subject.ID(), resolved); err != nil {
		return rpc.Outcome[SelectModelValue]{}, err
	}
	return rpc.OK(SelectModelValue{
		Selected: modelSelectionValue(resolved),
	}), nil
}

func modelSelectionValue(selected llm.CallConfig) ModelSelection {
	return ModelSelection{
		Provider:        selected.Provider,
		Model:           selected.Model,
		ReasoningEffort: string(selected.ReasoningEffort),
	}
}

func agentContainsImage(ctx context.Context, subject Agent) (bool, error) {
	pending, err := subject.Pending(ctx)
	if err != nil {
		return false, err
	}
	for _, entry := range append(pending.NextTurn, pending.NextStep...) {
		if agentmessage.ContentHasImage(entry.ContentValue()) {
			return true, nil
		}
	}
	snapshot, err := subject.Snapshot(ctx)
	if err != nil {
		return false, err
	}
	history, err := session.NewLog(snapshot.Header.ID, session.CreateOptions{
		Seed: snapshot.Events,
	})
	if err != nil {
		return false, err
	}
	messages, err := history.DeriveMessages()
	if err != nil {
		return false, err
	}
	for _, entry := range messages {
		if agentmessage.ContentHasImage(entry.ContentValue()) {
			return true, nil
		}
	}
	return false, nil
}
