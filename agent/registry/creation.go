package registry

import (
	"errors"
	middlewareactor "github.com/gorenx/goren/agent/middleware/actor"
	"reflect"
	"regexp"
	"slices"
	"time"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/session"
	sessionactor "github.com/gorenx/goren/session/actor"
	toolregistry "github.com/gorenx/goren/tools/registry"
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
	ModelRouter     *goakt.PID
	Middleware      *goakt.GrainIdentity
	Handlers        middlewareactor.Handlers
	PromptRegistry  *goakt.GrainIdentity
	ToolsRegistry   *goakt.GrainIdentity
	// Tools overrides the registered defaults for this Agent lifetime.
	Tools *toolregistry.Catalog
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

// FindAgent verifies the existing activation before returning a ready lifetime.
// Framework reactivation does not authorize a replacement business instance.
type FindAgent struct {
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

var sessionName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]*$`)

// validate checks construction input; parent admission remains a Registry rule.
func (input CreateAgent) validate() error {
	err := input.Handlers.Validate()
	if err != nil {
		return err
	}
	if input.Storage == nil ||
		input.SessionRegistry == nil ||
		input.ModelConfig == nil ||
		input.ModelRouter == nil ||
		input.Middleware == nil ||
		input.PromptRegistry == nil ||
		input.ToolsRegistry == nil ||
		input.Timeout <= 0 {
		return errors.New("agent registry: complete creation dependencies are required")
	}
	if !sessionName.MatchString(string(input.Creation.AgentID)) ||
		len(sessionactor.RuntimeName(string(input.Creation.AgentID))) > 255 {
		return errors.New("agent registry: SessionID cannot form a valid runtime address")
	}
	seen := make(map[string]struct{}, len(input.Components))
	for _, route := range input.Components {
		if route == nil {
			return errors.New("agent registry: nil component owner")
		}
		err := route.Validate()
		if err != nil {
			return err
		}
		_, found := seen[route.String()]
		if found {
			return errors.New("agent registry: duplicate component owner")
		}
		seen[route.String()] = struct{}{}
	}
	return nil
}

func (input CreateAgent) clone() CreateAgent {
	input.Options = input.Options.Clone()
	input.Handlers = input.Handlers.Clone()
	if input.Tools != nil {
		captured := input.Tools.Clone()
		input.Tools = &captured
	}
	if input.Parent != nil {
		parent := *input.Parent
		input.Parent = &parent
	}
	input.Components = slices.Clone(input.Components)
	return input
}

// matches prevents a repeated creation request from changing frozen configuration.
func (original CreateAgent) matches(input CreateAgent) bool {
	if original.Creation != input.Creation ||
		!reflect.DeepEqual(original.Parent, input.Parent) ||
		original.Restore != input.Restore ||
		!reflect.DeepEqual(original.Options, input.Options) ||
		!reflect.DeepEqual(original.Storage, input.Storage) ||
		original.Timeout != input.Timeout ||
		!reflect.DeepEqual(original.Tools, input.Tools) ||
		!sameRoute(original.SessionRegistry, input.SessionRegistry) ||
		!sameRoute(original.ModelConfig, input.ModelConfig) ||
		original.ModelRouter != input.ModelRouter ||
		!reflect.DeepEqual(original.Handlers, input.Handlers) ||
		!sameRoute(original.Middleware, input.Middleware) ||
		!sameRoute(original.PromptRegistry, input.PromptRegistry) ||
		!sameRoute(original.ToolsRegistry, input.ToolsRegistry) ||
		!sameRoute(original.Commands, input.Commands) ||
		len(original.Components) != len(input.Components) {
		return false
	}
	for index, route := range original.Components {
		if !sameRoute(route, input.Components[index]) {
			return false
		}
	}
	return true
}

func sameRoute(left, right *goakt.GrainIdentity) bool {
	if left == nil || right == nil {
		return left == right
	}
	return left.Equal(right)
}
