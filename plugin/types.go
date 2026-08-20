// Package plugin provides the typed service, Waterfall, Event, and lifecycle
// contracts used to assemble Goren from statically linked Go plugins.
package plugin

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

var (
	// ErrServiceUnavailable reports a violated hard-dependency invariant or an
	// unavailable optional Service.
	ErrServiceUnavailable = errors.New("plugin: service is unavailable")
	// ErrServiceConflict reports two active providers for the same Service in
	// one exact Scope.
	ErrServiceConflict = errors.New("plugin: service provider conflicts with an active provider")
	// ErrContextClosed reports an operation attempted through a stopped Fiber.
	ErrContextClosed = errors.New("plugin: runtime context is closed")
	// ErrRegistrationClosed reports a registration attempted after the current
	// Plugin.Apply mount transaction has closed.
	ErrRegistrationClosed = errors.New("plugin: registration is only allowed during Plugin.Apply")
	// ErrDependencyResolutionClosed reports Service dependency resolution after
	// the current Plugin.Apply activation snapshot has closed.
	ErrDependencyResolutionClosed = errors.New("plugin: Service dependencies are only resolved during Plugin.Apply")
	// ErrPluginNotActive reports a lifecycle operation attempted through a
	// Context whose Fiber is neither applying nor active.
	ErrPluginNotActive = errors.New("plugin: child plugins require an active parent Fiber")
	// ErrWaterfallAlreadyProceeded reports a Middleware invoking its downstream
	// chain more than once for one invocation.
	ErrWaterfallAlreadyProceeded = errors.New("plugin: Waterfall already proceeded")
)

// Service marks an owner-defined long-lived business capability. Provider
// interfaces embed Service; DTOs, primitives, and functions do not satisfy it.
type Service interface {
	RuntimeService()
}

// ServiceBase lets a concrete provider satisfy Service by embedding it.
type ServiceBase struct{}

// RuntimeService implements Service.
func (ServiceBase) RuntimeService() {}

// Event marks an owner-defined fact that has already occurred.
type Event interface {
	PluginEvent()
}

// EventBase lets a named fact type satisfy Event by embedding it.
type EventBase struct{}

// PluginEvent implements Event.
func (EventBase) PluginEvent() {}

// WaterfallInput marks an owner-defined input of one interceptable operation.
type WaterfallInput interface {
	RuntimeWaterfallInput()
}

// WaterfallInputBase lets a named input satisfy WaterfallInput by embedding it.
type WaterfallInputBase struct{}

// RuntimeWaterfallInput implements WaterfallInput.
func (WaterfallInputBase) RuntimeWaterfallInput() {}

// WaterfallOutput marks an owner-defined output of one interceptable operation.
type WaterfallOutput interface {
	RuntimeWaterfallOutput()
}

// WaterfallOutputBase lets a named output satisfy WaterfallOutput by embedding it.
type WaterfallOutputBase struct{}

// RuntimeWaterfallOutput implements WaterfallOutput.
func (WaterfallOutputBase) RuntimeWaterfallOutput() {}

// Plugin is one statically linked module and the lifecycle owner of its
// activation-local resources. Runtime may call Apply and Dispose repeatedly in
// sequential dependency settlement cycles; Dispose must be idempotent and
// restore the Plugin to an inactive state even after a partial Apply failure.
type Plugin interface {
	Manifest() Manifest
	Apply(applyContext context.Context, pluginContext *Context) error
	Dispose(disposeContext context.Context) error
}

// Manifest declares a Plugin's stable identity and Service dependencies.
type Manifest struct {
	Name     string
	Provides []ServiceDefinition
	Requires []ServiceDefinition
	Optional []ServiceDefinition
}

type manifestSpec struct {
	Name     string
	Provides []serviceRef
	Requires []serviceRef
	Optional []serviceRef
}

// FiberID identifies one concrete Plugin activation attempt inside a Runtime.
type FiberID uint64

// FiberState is the externally observable lifecycle state of one Fiber.
type FiberState string

const (
	FiberWaiting     FiberState = "waiting-dependencies"
	FiberStarting    FiberState = "starting"
	FiberActive      FiberState = "active"
	FiberStopping    FiberState = "stopping"
	FiberStopped     FiberState = "stopped"
	FiberRollingBack FiberState = "rolling-back"
	FiberFailed      FiberState = "failed"
)

// FiberStatus is an immutable diagnostics view of one mounted Plugin and its
// current activation attempt.
type FiberStatus struct {
	HandleID     uint64
	FiberID      FiberID
	Name         string
	State        FiberState
	Dependencies []ServiceDependencyStatus
	Services     []ServiceBindingStatus
	Waterfalls   []WaterfallBindingStatus
	Events       []EventSubscriptionStatus
	Effects      []string
	Missing      []string
	Error        error
}

// ServiceDependencyStatus describes the concrete provider selected for one
// active Fiber dependency snapshot.
type ServiceDependencyStatus struct {
	Service         string
	ProviderFiberID FiberID
	Optional        bool
}

// ServiceBindingStatus describes one active Service binding owned by a Fiber.
type ServiceBindingStatus struct {
	Service string
	Scope   ScopeKey
}

// WaterfallBindingStatus describes one active Middleware binding.
type WaterfallBindingStatus struct {
	Waterfall string
	Scope     ScopeKey
}

// EventSubscriptionStatus describes one active Observer subscription.
type EventSubscriptionStatus struct {
	Event string
	Scope ScopeKey
}

// Handle identifies one mounted Plugin. A Handle can explicitly stop a
// dynamically mounted subtree before its owning Fiber stops.
type Handle struct {
	owner *Runtime
	id    uint64
}

// ID returns the Runtime-local mount identifier.
func (pluginHandle Handle) ID() uint64 {
	return pluginHandle.id
}

// Stop stops the mounted Plugin and its owned Fiber tree.
func (pluginHandle Handle) Stop(stopContext context.Context) error {
	if pluginHandle.owner == nil {
		return errors.New("plugin: stop through invalid Handle")
	}
	return pluginHandle.owner.Unload(stopContext, pluginHandle)
}

type scopeToken struct {
	parent *scopeToken
	depth  int
}

// ScopeKey is an opaque comparable routing identity. Its zero value denotes
// the Runtime root scope.
type ScopeKey struct {
	token *scopeToken
}

// IsGlobal reports whether the key identifies the Runtime root scope.
func (selectedKey ScopeKey) IsGlobal() bool {
	return selectedKey.token == nil
}

// ScopeLineage returns child keys from the farthest ancestor to selectedKey.
// Root ownership is intentionally omitted.
func ScopeLineage(selectedKey ScopeKey) []ScopeKey {
	tokens := make([]*scopeToken, 0)
	for currentToken := selectedKey.token; currentToken != nil; currentToken = currentToken.parent {
		tokens = append(tokens, currentToken)
	}
	lineage := make([]ScopeKey, len(tokens))
	for tokenIndex := range tokens {
		lineage[len(tokens)-1-tokenIndex] = ScopeKey{
			token: tokens[tokenIndex],
		}
	}
	return lineage
}

func normalizeManifest(metadata Manifest) (manifestSpec, error) {
	if strings.TrimSpace(metadata.Name) == "" || metadata.Name != strings.TrimSpace(metadata.Name) {
		return manifestSpec{}, errors.New("plugin: manifest name must be non-empty and trimmed")
	}
	seen := make(map[string]string)
	providedRefs, err := normalizeServiceDefinitions(
		metadata.Name,
		"provides",
		metadata.Provides,
		seen,
	)
	if err != nil {
		return manifestSpec{}, err
	}
	requiredRefs, err := normalizeServiceDefinitions(
		metadata.Name,
		"requires",
		metadata.Requires,
		seen,
	)
	if err != nil {
		return manifestSpec{}, err
	}
	optionalRefs, err := normalizeServiceDefinitions(
		metadata.Name,
		"optional",
		metadata.Optional,
		seen,
	)
	if err != nil {
		return manifestSpec{}, err
	}
	return manifestSpec{
		Name:     metadata.Name,
		Provides: providedRefs,
		Requires: requiredRefs,
		Optional: optionalRefs,
	}, nil
}

func normalizeServiceDefinitions(
	pluginName string,
	groupName string,
	definitions []ServiceDefinition,
	seen map[string]string,
) ([]serviceRef, error) {
	references := make([]serviceRef, 0, len(definitions))
	for _, declaredService := range definitions {
		if declaredService == nil {
			return nil, fmt.Errorf(
				"plugin: %s %s: invalid Service definition",
				pluginName,
				groupName,
			)
		}
		definitionRef := declaredService.serviceReference()
		if err := definitionRef.validate(); err != nil {
			return nil, fmt.Errorf(
				"plugin: %s %s: %w",
				pluginName,
				groupName,
				err,
			)
		}
		if previousGroup, exists := seen[definitionRef.name]; exists {
			return nil, fmt.Errorf(
				"plugin: %s declares service %q in both %s and %s",
				pluginName,
				definitionRef.name,
				previousGroup,
				groupName,
			)
		}
		seen[definitionRef.name] = groupName
		references = append(references, definitionRef)
	}
	return references, nil
}

func manifestContains(references []serviceRef, expectedRef serviceRef) bool {
	for _, candidateRef := range references {
		if candidateRef.sameDefinition(expectedRef) {
			return true
		}
	}
	return false
}

func sameServiceDefinitions(leftRefs []serviceRef, rightRefs []serviceRef) bool {
	if len(leftRefs) != len(rightRefs) {
		return false
	}
	for _, leftRef := range leftRefs {
		if !manifestContains(rightRefs, leftRef) {
			return false
		}
	}
	return true
}
