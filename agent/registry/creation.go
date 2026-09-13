package registry

import (
	"errors"
	"reflect"
	"regexp"
	"slices"
	"time"

	"github.com/gorenx/goren/agent"
	middlewareactor "github.com/gorenx/goren/agent/middleware/actor"
	"github.com/gorenx/goren/session"
	sessionactor "github.com/gorenx/goren/session/actor"
	"github.com/gorenx/goren/systemprompt"
	toolregistry "github.com/gorenx/goren/tools/registry"
	goakt "github.com/tochemey/goakt/v4/actor"
)

// CreateAgent starts one exact lifetime. Concurrent retries share the first input;
// a failed creation remains reserved until CloseAgent confirms its cleanup.
type CreateAgent struct {
	// Manager owns child lifecycle decisions. Nil delegates configuration restarts to Registry.
	Manager *goakt.GrainIdentity
	// configuration is derived for this installation, never part of the caller contract.
	configuration     *toolregistry.Configuration
	Role              agent.Role
	Creation          Creation
	Parent            *Creation
	Restore           bool
	Options           session.CreateOptions
	AgentOptions      *agent.Options
	Storage           sessionactor.RecoveryStorage
	SessionRegistry   *goakt.GrainIdentity
	ModelConfig       *goakt.GrainIdentity
	ModelRouter       *goakt.PID
	Middleware        *goakt.GrainIdentity
	Handlers          middlewareactor.Handlers
	PromptRegistry    *goakt.GrainIdentity
	ToolsRegistry     *goakt.GrainIdentity
	Settings          *goakt.GrainIdentity
	ConfigurationName string
	// Tools overrides the registered defaults for this Agent lifetime.
	Tools *toolregistry.Catalog
	// Prompt selects an independent prompt baseline; nil uses the shared registry.
	Prompt *systemprompt.Config
	// PromptSections are local definitions captured for this Agent lifetime.
	PromptSections []systemprompt.PromptSection
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

// CreateChildAgent derives one child from the exact parent's frozen creation
// input. The Registry remains the sole owner of composition dependencies and
// parent-child admission.
type CreateChildAgent struct {
	// Manager receives restart requests for this exact child lifetime.
	Manager        *goakt.GrainIdentity
	ConfigName     string
	Role           agent.Role
	Parent         Creation
	Child          Creation
	Restore        bool
	Options        session.CreateOptions
	AgentOptions   *agent.Options
	Prompt         *systemprompt.Config
	Tools          *toolregistry.Catalog
	PromptSections []systemprompt.PromptSection
	// Components replaces the parent's additional capability owners. Nil inherits
	// the frozen parent list; a non-nil empty slice installs no additional components.
	Components []*goakt.GrainIdentity
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
	if input.Manager != nil && input.Parent == nil {
		return errors.New("agent registry: lifecycle manager requires a parent")
	}
	if !input.Role.Valid() || input.Role == agent.CompanionRole && input.Parent == nil {
		return agent.ErrRole
	}
	if input.Parent == nil && input.Options.Metadata.ParentSession != nil ||
		input.Parent != nil && input.Options.Metadata.ParentSession != nil &&
			*input.Options.Metadata.ParentSession != input.Parent.AgentID {
		return errors.New("agent registry: Session parent does not match creation")
	}
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
	if input.AgentOptions != nil {
		captured := input.AgentOptions.Clone()
		input.AgentOptions = &captured
	}
	if input.Prompt != nil {
		captured := input.Prompt.Clone()
		input.Prompt = &captured
	}
	input.Handlers = input.Handlers.Clone()
	input.PromptSections = slices.Clone(input.PromptSections)
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
	if !sameRoute(original.Manager, input.Manager) || original.Role != input.Role ||
		original.Creation != input.Creation ||
		!reflect.DeepEqual(original.Parent, input.Parent) ||
		original.Restore != input.Restore ||
		!reflect.DeepEqual(original.Options, input.Options) ||
		!reflect.DeepEqual(original.AgentOptions, input.AgentOptions) ||
		!reflect.DeepEqual(original.Storage, input.Storage) ||
		original.Timeout != input.Timeout ||
		!reflect.DeepEqual(original.Tools, input.Tools) ||
		!reflect.DeepEqual(original.PromptSections, input.PromptSections) ||
		!reflect.DeepEqual(original.Prompt, input.Prompt) ||
		!sameRoute(original.SessionRegistry, input.SessionRegistry) ||
		!sameRoute(original.ModelConfig, input.ModelConfig) ||
		original.ModelRouter != input.ModelRouter ||
		!sameMiddlewareRoutes(original.Handlers.PreStep, input.Handlers.PreStep) ||
		!sameMiddlewareRoutes(original.Handlers.RequestError, input.Handlers.RequestError) ||
		!sameMiddlewareRoutes(original.Handlers.TurnStopping, input.Handlers.TurnStopping) ||
		!sameRoute(original.Middleware, input.Middleware) ||
		!sameRoute(original.PromptRegistry, input.PromptRegistry) ||
		!sameRoute(original.ToolsRegistry, input.ToolsRegistry) ||
		!sameRoute(original.Settings, input.Settings) ||
		original.ConfigurationName != input.ConfigurationName ||
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

// Middleware selections compare registration order and routing identity, not
// framework-internal validation state retained by each GrainIdentity.
func sameMiddlewareRoutes(left, right []middlewareactor.Route) bool {
	return slices.EqualFunc(left, right, func(first, second middlewareactor.Route) bool {
		return first.Definition == second.Definition && sameRoute(first.Target, second.Target)
	})
}
