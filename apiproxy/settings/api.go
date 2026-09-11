package settingsapi

import (
	"context"
	"encoding/json"

	rpc "github.com/gorenx/goren/apiproxy/rpc"
	"github.com/gorenx/goren/settings"
	"github.com/gorenx/goren/tools"
)

// Settings methods share the Host privileged access boundary.
const (
	DescribeMethod         = "settings.describe"
	UpdateToolMethod       = "settings.updateTool"
	ResetToolMethod        = "settings.resetTool"
	UpdateProviderMethod   = "settings.updateProvider"
	UpdatePromptMethod     = "settings.updatePrompt"
	UpdateMCPMethod        = "settings.updateMCP"
	ResetProviderMethod    = "settings.resetProvider"
	ResetPromptMethod      = "settings.resetPrompt"
	ResetMCPMethod         = "settings.resetMCP"
	RetryApplicationMethod = "settings.retryApplication"
)

// DescribeRequest is the empty settings.describe payload.
type DescribeRequest struct{}

// ApplyMode identifies when an accepted setting takes effect.
type ApplyMode string

const (
	ApplyLive    ApplyMode = "live"
	ApplyRestart ApplyMode = "restart"
)

// SecretView reports a redacted secret slot without its value.
type SecretView struct {
	Path []string `json:"path"`
	Set  bool     `json:"set"`
}

// NamespaceView is one redacted Settings namespace snapshot.
type NamespaceView struct {
	NS              string           `json:"ns"`
	Name            string           `json:"name"`
	Schema          json.RawMessage  `json:"schema"`
	Value           json.RawMessage  `json:"value"`
	Base            *json.RawMessage `json:"base,omitempty"`
	User            *json.RawMessage `json:"user,omitempty"`
	Applies         ApplyMode        `json:"applies"`
	Secrets         []SecretView     `json:"secrets"`
	Revision        int64            `json:"revision"`
	AppliedRevision *int64           `json:"appliedRevision,omitempty"`
	PendingRestart  bool             `json:"pendingRestart,omitempty"`
	ApplyError      string           `json:"applyError,omitempty"`
}

// DescribeValue is the complete browser-visible Settings catalog.
type DescribeValue struct {
	Writable    bool            `json:"writable"`
	HasDocument bool            `json:"hasDocument"`
	Namespaces  []NamespaceView `json:"namespaces"`
}

// UpdateProviderRequest replaces one complete named configuration.
type UpdateProviderRequest struct {
	Name             string                  `json:"name"`
	ExpectedRevision int64                   `json:"expectedRevision"`
	Config           settings.ProviderConfig `json:"config"`
}

// ResetProviderRequest clears the complete named override.
type ResetProviderRequest struct {
	Name             string `json:"name"`
	ExpectedRevision int64  `json:"expectedRevision"`
}

// UpdatePromptRequest replaces one complete named configuration.
type UpdatePromptRequest struct {
	Name             string                `json:"name"`
	ExpectedRevision int64                 `json:"expectedRevision"`
	Config           settings.PromptConfig `json:"config"`
}

// ResetPromptRequest clears the complete named override.
type ResetPromptRequest struct {
	Name             string `json:"name"`
	ExpectedRevision int64  `json:"expectedRevision"`
}

// UpdateMCPRequest replaces one complete named configuration.
type UpdateMCPRequest struct {
	Name             string             `json:"name"`
	ExpectedRevision int64              `json:"expectedRevision"`
	Config           settings.MCPConfig `json:"config"`
}

// ResetMCPRequest clears the complete named override.
type ResetMCPRequest struct {
	Name             string `json:"name"`
	ExpectedRevision int64  `json:"expectedRevision"`
}

// UpdateToolRequest replaces the complete editable configuration of one tool.
type UpdateToolRequest struct {
	Name             string       `json:"name"`
	ExpectedRevision int64        `json:"expectedRevision"`
	Config           tools.Config `json:"config"`
}

// ResetToolRequest clears one complete named tool override.
type ResetToolRequest struct {
	Name             string `json:"name"`
	ExpectedRevision int64  `json:"expectedRevision"`
}

// RetryApplicationRequest identifies one saved group to apply again.
type RetryApplicationRequest struct {
	NS               string `json:"ns"`
	Name             string `json:"name"`
	ExpectedRevision int64  `json:"expectedRevision"`
}

// API exposes the catalog and distinct Settings business operations.
type API interface {
	UpdateTool(context.Context, rpc.Request[UpdateToolRequest]) (rpc.Outcome[NamespaceView], error)
	ResetTool(context.Context, rpc.Request[ResetToolRequest]) (rpc.Outcome[NamespaceView], error)
	Describe(context.Context, rpc.Request[DescribeRequest]) (rpc.Outcome[DescribeValue], error)
	UpdateProvider(context.Context, rpc.Request[UpdateProviderRequest]) (rpc.Outcome[NamespaceView], error)
	UpdatePrompt(context.Context, rpc.Request[UpdatePromptRequest]) (rpc.Outcome[NamespaceView], error)
	UpdateMCP(context.Context, rpc.Request[UpdateMCPRequest]) (rpc.Outcome[NamespaceView], error)
	ResetProvider(context.Context, rpc.Request[ResetProviderRequest]) (rpc.Outcome[NamespaceView], error)
	ResetPrompt(context.Context, rpc.Request[ResetPromptRequest]) (rpc.Outcome[NamespaceView], error)
	ResetMCP(context.Context, rpc.Request[ResetMCPRequest]) (rpc.Outcome[NamespaceView], error)
	RetryApplication(context.Context, rpc.Request[RetryApplicationRequest]) (rpc.Outcome[NamespaceView], error)
}

// Register installs the catalog, typed updates, resets and application retry endpoint.
func Register(methods *rpc.Catalog, endpoint API) error {
	if err := rpc.RegisterUnary(methods, DescribeMethod, rpc.DecodeObject[DescribeRequest], endpoint.Describe); err != nil {
		return err
	}
	if err := rpc.RegisterUnary(methods, UpdateProviderMethod, DecodeUpdateProviderRequest, endpoint.UpdateProvider); err != nil {
		return err
	}
	if err := rpc.RegisterUnary(methods, UpdatePromptMethod, DecodeUpdatePromptRequest, endpoint.UpdatePrompt); err != nil {
		return err
	}
	if err := rpc.RegisterUnary(methods, UpdateMCPMethod, DecodeUpdateMCPRequest, endpoint.UpdateMCP); err != nil {
		return err
	}
	if err := rpc.RegisterUnary(methods, ResetProviderMethod, DecodeResetProviderRequest, endpoint.ResetProvider); err != nil {
		return err
	}
	if err := rpc.RegisterUnary(methods, ResetPromptMethod, DecodeResetPromptRequest, endpoint.ResetPrompt); err != nil {
		return err
	}
	if err := rpc.RegisterUnary(methods, ResetMCPMethod, DecodeResetMCPRequest, endpoint.ResetMCP); err != nil {
		return err
	}
	if err := rpc.RegisterUnary(methods, UpdateToolMethod, DecodeUpdateToolRequest, endpoint.UpdateTool); err != nil {
		return err
	}
	if err := rpc.RegisterUnary(methods, ResetToolMethod, DecodeResetToolRequest, endpoint.ResetTool); err != nil {
		return err
	}
	return rpc.RegisterUnary(methods, RetryApplicationMethod, DecodeRetryApplicationRequest, endpoint.RetryApplication)
}
