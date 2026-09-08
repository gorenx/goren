package registry

import (
	"time"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/session"
	sessionactor "github.com/gorenx/goren/session/actor"
	goakt "github.com/tochemey/goakt/v4/actor"
)

// CreateAgent starts one exact lifetime. Concurrent retries share the first input;
// a failed creation remains reserved until CloseAgent confirms its cleanup.
type CreateAgent struct {
	Creation        Creation
	Parent          *Creation
	Restore         bool
	Options         session.CreateOptions
	Storage         sessionactor.RecoveryStorage
	SessionRegistry *goakt.GrainIdentity
	ModelConfig     *goakt.GrainIdentity
	PromptRegistry  *goakt.GrainIdentity
	ToolsRegistry   *goakt.GrainIdentity
	// Commands enables the independent command runtime when its global Registry is supplied.
	Commands *goakt.GrainIdentity
	// Components are capability Registry routes in startup order.
	Components []*goakt.GrainIdentity
	Timeout    time.Duration
}

// Component pairs a capability Registry with the runtime it initialized.
type Component struct {
	Owner   *goakt.GrainIdentity
	Runtime *goakt.GrainIdentity
}

// Resolve verifies the existing activation before returning a ready lifetime.
// Framework reactivation does not authorize a replacement business instance.
type Resolve struct {
	AgentID session.SessionID
	// WaitReady shares the final result of an existing initialization. It does
	// not create a missing lifetime or authorize a replacement activation.
	WaitReady bool
}

// Ready contains routes from one completed creation. An error exposes no routes.
// Callers must retain CreationID in subsequent business requests.
type Ready struct {
	Creation     Creation
	Registration agent.Registration
	Executor     *goakt.GrainIdentity
	Commands     *goakt.GrainIdentity
	Components   []Component
	Err          error
}
