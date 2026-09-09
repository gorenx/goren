package agentactor

import (
	"time"

	"github.com/gorenx/goren/session"
	goakt "github.com/tochemey/goakt/v4/actor"
)

// CreateTool starts a tool under the current Agent reservation. It does not
// construct the tool implementation or publish its model-facing definition.
type CreateTool struct {
	AgentID         session.SessionID
	AgentCreationID string
	Name            string
	Registry        *goakt.GrainIdentity
	Timeout         time.Duration
}

// ToolInstance returns the tool lifetime used by subsequent ToolActor requests.
// CreationID belongs to the tool and changes after removal and recreation.
type ToolInstance struct {
	Identity   *goakt.GrainIdentity
	CreationID string
	Err        error
}

// RemoveTool closes and deactivates one exact tool lifetime. An older lifetime
// cannot remove a replacement that reuses the logical tool name.
type RemoveTool struct {
	AgentID         session.SessionID
	AgentCreationID string
	Name            string
	ToolCreationID  string
}

// ToolRemoved confirms both resource closure and framework deactivation.
type ToolRemoved struct {
	Err error
}
