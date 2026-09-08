package presetapi

import rpc "github.com/gorenx/goren/apiproxy/rpc"

import "context"

// ListMethod is the ordinary deployment-roster endpoint.
const ListMethod = "agentPreset.list"

// ListRequest is the empty agentPreset.list payload.
type ListRequest struct{}

// Trust distinguishes shipped and locally authored presets.
type Trust string

const (
	System Trust = "system"
	User   Trust = "user"
)

// Entry is one browser-visible preset roster row.
type Entry struct {
	ID          string  `json:"id"`
	Trust       Trust   `json:"trust"`
	IsDefault   bool    `json:"isDefault"`
	Name        *string `json:"name,omitempty"`
	Description *string `json:"description,omitempty"`
	Broken      *string `json:"broken,omitempty"`
}

// ListValue describes the current deployment roster.
type ListValue struct {
	Presets     []Entry `json:"presets"`
	Authorable  bool    `json:"authorable"`
	HasDocument bool    `json:"hasDocument"`
}

// API owns the currently included read-only roster method.
type API interface {
	List(context.Context, rpc.Request[ListRequest]) (rpc.Outcome[ListValue], error)
}

// Register installs agentPreset.list without claiming the
// separately scoped authoring and session-recomposition methods.
func Register(methods *rpc.Catalog, endpoint API) error {
	return rpc.RegisterUnary(methods, ListMethod, rpc.DecodeObject[ListRequest], endpoint.List)
}
