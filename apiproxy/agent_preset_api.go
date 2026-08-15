package apiproxy

import "context"

// AgentPresetListMethod is the ordinary deployment-roster endpoint.
const AgentPresetListMethod = "agentPreset.list"

// AgentPresetListRequest is the empty agentPreset.list payload.
type AgentPresetListRequest struct{}

// AgentPresetTrust distinguishes shipped and locally authored presets.
type AgentPresetTrust string

const (
	AgentPresetSystem AgentPresetTrust = "system"
	AgentPresetUser   AgentPresetTrust = "user"
)

// AgentPresetEntry is one browser-visible preset roster row.
type AgentPresetEntry struct {
	ID          string           `json:"id"`
	Trust       AgentPresetTrust `json:"trust"`
	IsDefault   bool             `json:"isDefault"`
	Name        *string          `json:"name,omitempty"`
	Description *string          `json:"description,omitempty"`
	Broken      *string          `json:"broken,omitempty"`
}

// AgentPresetListValue describes the current deployment roster.
type AgentPresetListValue struct {
	Presets     []AgentPresetEntry `json:"presets"`
	Authorable  bool               `json:"authorable"`
	HasDocument bool               `json:"hasDocument"`
}

// AgentPresetListAPI owns the currently included read-only roster method.
type AgentPresetListAPI interface {
	List(context.Context, Request[AgentPresetListRequest]) (Outcome[AgentPresetListValue], error)
}

// RegisterAgentPresetListAPI installs agentPreset.list without claiming the
// separately scoped authoring and session-recomposition methods.
func RegisterAgentPresetListAPI(methods *Catalog, gateway AgentPresetListAPI) error {
	return RegisterUnary(methods, AgentPresetListMethod, DecodeObject[AgentPresetListRequest], gateway.List)
}
