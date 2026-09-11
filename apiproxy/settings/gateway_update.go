package settingsapi

import (
	"context"
	"errors"

	"github.com/gorenx/goren/apiproxy/rpc"
	"github.com/gorenx/goren/connection"
	"github.com/gorenx/goren/settings"
)

// UpdateAgent saves a named selection; applying it belongs to the Agent use case.
func (owner *Gateway) UpdateAgent(ctx context.Context, call rpc.Request[UpdateAgentRequest]) (rpc.Outcome[NamespaceView], error) {
	if owner.service == nil {
		return absentSettings(), nil
	}
	projection, err := owner.service.UpdateAgent(ctx, call.Payload.Name, call.Payload.ExpectedRevision, call.Payload.Config)
	return updateOutcome("agent", call.Payload.Name, call.Payload.ExpectedRevision, projection, err)
}

// UpdateProvider forwards a complete named replacement to the Settings owner.
func (owner *Gateway) UpdateProvider(ctx context.Context, call rpc.Request[UpdateProviderRequest]) (rpc.Outcome[NamespaceView], error) {
	if owner.service == nil {
		return absentSettings(), nil
	}
	projection, err := owner.service.UpdateProvider(ctx, call.Payload.Name, call.Payload.ExpectedRevision, call.Payload.Config)
	return updateOutcome("llmprovider", call.Payload.Name, call.Payload.ExpectedRevision, projection, err)
}

// UpdateTool forwards a complete named replacement to the Settings owner.
func (owner *Gateway) UpdateTool(ctx context.Context, call rpc.Request[UpdateToolRequest]) (rpc.Outcome[NamespaceView], error) {
	if owner.service == nil {
		return absentSettings(), nil
	}
	projection, err := owner.service.UpdateTool(ctx, call.Payload.Name, call.Payload.ExpectedRevision, call.Payload.Config)
	return updateOutcome("tool", call.Payload.Name, call.Payload.ExpectedRevision, projection, err)
}

// UpdatePrompt forwards a complete named replacement to the Settings owner.
func (owner *Gateway) UpdatePrompt(ctx context.Context, call rpc.Request[UpdatePromptRequest]) (rpc.Outcome[NamespaceView], error) {
	if owner.service == nil {
		return absentSettings(), nil
	}
	projection, err := owner.service.UpdatePrompt(ctx, call.Payload.Name, call.Payload.ExpectedRevision, call.Payload.Config)
	return updateOutcome("prompt", call.Payload.Name, call.Payload.ExpectedRevision, projection, err)
}

// UpdateMCP forwards a complete named replacement to the Settings owner.
func (owner *Gateway) UpdateMCP(ctx context.Context, call rpc.Request[UpdateMCPRequest]) (rpc.Outcome[NamespaceView], error) {
	if owner.service == nil {
		return absentSettings(), nil
	}
	projection, err := owner.service.UpdateMCP(ctx, call.Payload.Name, call.Payload.ExpectedRevision, call.Payload.Config)
	return updateOutcome("mcp", call.Payload.Name, call.Payload.ExpectedRevision, projection, err)
}

// ResetProvider forwards complete named override removal to its Settings owner.
func (owner *Gateway) ResetProvider(ctx context.Context, call rpc.Request[ResetProviderRequest]) (rpc.Outcome[NamespaceView], error) {
	if owner.service == nil {
		return absentSettings(), nil
	}
	projection, err := owner.service.ResetProvider(ctx, call.Payload.Name, call.Payload.ExpectedRevision)
	return updateOutcome("llmprovider", call.Payload.Name, call.Payload.ExpectedRevision, projection, err)
}

// ResetTool forwards complete named override removal to its Settings owner.
func (owner *Gateway) ResetTool(ctx context.Context, call rpc.Request[ResetToolRequest]) (rpc.Outcome[NamespaceView], error) {
	if owner.service == nil {
		return absentSettings(), nil
	}
	projection, err := owner.service.ResetTool(ctx, call.Payload.Name, call.Payload.ExpectedRevision)
	return updateOutcome("tool", call.Payload.Name, call.Payload.ExpectedRevision, projection, err)
}

// ResetPrompt forwards complete named override removal to its Settings owner.
func (owner *Gateway) ResetPrompt(ctx context.Context, call rpc.Request[ResetPromptRequest]) (rpc.Outcome[NamespaceView], error) {
	if owner.service == nil {
		return absentSettings(), nil
	}
	projection, err := owner.service.ResetPrompt(ctx, call.Payload.Name, call.Payload.ExpectedRevision)
	return updateOutcome("prompt", call.Payload.Name, call.Payload.ExpectedRevision, projection, err)
}

// ResetMCP clears one complete named override through the Settings owner.
func (owner *Gateway) ResetMCP(ctx context.Context, call rpc.Request[ResetMCPRequest]) (rpc.Outcome[NamespaceView], error) {
	if owner.service == nil {
		return absentSettings(), nil
	}
	projection, err := owner.service.ResetMCP(ctx, call.Payload.Name, call.Payload.ExpectedRevision)
	return updateOutcome("mcp", call.Payload.Name, call.Payload.ExpectedRevision, projection, err)
}

// RetryApplication forwards an application retry without rewriting user overrides.
func (owner *Gateway) RetryApplication(ctx context.Context, call rpc.Request[RetryApplicationRequest]) (rpc.Outcome[NamespaceView], error) {
	if owner.service == nil {
		return absentSettings(), nil
	}
	section := businessSection(call.Payload.NS)
	if section == 0 {
		return rejectedUpdate(call.Payload.NS, connection.ErrorSettingsNotExposed, "Settings group is not exposed"), nil
	}
	projection, err := owner.service.RetryApplication(ctx, settings.Group{
		Section: section,
		Name:    call.Payload.Name,
	}, call.Payload.ExpectedRevision)
	return updateOutcome(call.Payload.NS, call.Payload.Name, call.Payload.ExpectedRevision, projection, err)
}

// updateOutcome maps owner results only; revision checks and persistence belong to Settings.
func updateOutcome(namespace string, name string, expected int64, projection settings.Snapshot, failure error) (rpc.Outcome[NamespaceView], error) {
	if errors.Is(failure, context.Canceled) || errors.Is(failure, context.DeadlineExceeded) {
		return rpc.Outcome[NamespaceView]{}, failure
	}
	if errors.Is(failure, settings.ErrConflict) {
		actual := projection.State.Version(settings.Group{
			Section: businessSection(namespace),
			Name:    name,
		})
		return rpc.Fail[NamespaceView](rpc.NewRPCError(connection.ErrorSettingsConflict, "Settings changed; reload before saving", struct {
			NS       string `json:"ns"`
			Name     string `json:"name"`
			Expected int64  `json:"expected"`
			Actual   int64  `json:"actual"`
		}{
			NS:       namespace,
			Name:     name,
			Expected: expected,
			Actual:   actual,
		})), nil
	}
	if failure != nil && !errors.Is(failure, settings.ErrApplication) {
		message := "Settings operation failed"
		if errors.Is(failure, settings.ErrUncertain) {
			message = "Save outcome is uncertain; reload settings before retrying"
		}
		return rejectedUpdate(namespace, connection.ErrorSettingsRejected, message), nil
	}
	view, err := groupView(projection, namespace, name)
	if err != nil {
		return rpc.Outcome[NamespaceView]{}, err
	}
	return rpc.OK(view), nil
}

func absentSettings() rpc.Outcome[NamespaceView] {
	return rpc.Fail[NamespaceView](rpc.NewRPCError(connection.ErrorInternal, settingsAbsentMessage, struct{}{}))
}

func rejectedUpdate(namespace string, code connection.RPCErrorCode, message string) rpc.Outcome[NamespaceView] {
	return rpc.Fail[NamespaceView](rpc.NewRPCError(code, message, struct {
		NS string `json:"ns"`
	}{
		NS: namespace,
	}))
}

// UpdateCompanion forwards a complete named replacement to the Settings owner.
func (owner *Gateway) UpdateCompanion(ctx context.Context, call rpc.Request[UpdateCompanionRequest]) (rpc.Outcome[NamespaceView], error) {
	if owner.service == nil {
		return absentSettings(), nil
	}
	projection, err := owner.service.UpdateCompanion(ctx, call.Payload.Name, call.Payload.ExpectedRevision, call.Payload.Config)
	return updateOutcome("companion", call.Payload.Name, call.Payload.ExpectedRevision, projection, err)
}

// ResetCompanion clears one complete named override through the Settings owner.
func (owner *Gateway) ResetCompanion(ctx context.Context, call rpc.Request[ResetCompanionRequest]) (rpc.Outcome[NamespaceView], error) {
	if owner.service == nil {
		return absentSettings(), nil
	}
	projection, err := owner.service.ResetCompanion(ctx, call.Payload.Name, call.Payload.ExpectedRevision)
	return updateOutcome("companion", call.Payload.Name, call.Payload.ExpectedRevision, projection, err)
}
