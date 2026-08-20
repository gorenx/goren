// Package plugin provides a statically linked, scoped Plugin Runtime with
// typed Services, Events, and Waterfalls.
package plugin

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
)

var (
	// ErrServiceUnavailable reports that a declared Service dependency has no
	// active provider visible from the Plugin's Scope.
	ErrServiceUnavailable = errors.New("plugin: service is unavailable")
	// ErrServiceConflict reports multiple Service providers in one exact Scope.
	ErrServiceConflict = errors.New("plugin: service provider conflicts with an active provider")
	// ErrPluginNotBound reports use of a Plugin that is not mounted.
	ErrPluginNotBound = errors.New("plugin: Plugin is not bound to a Runtime")
	// ErrPluginNotActive reports an operation that requires an active Plugin.
	ErrPluginNotActive = errors.New("plugin: Plugin is not active")
	// ErrDependencyResolutionClosed reports dependency resolution outside Apply.
	ErrDependencyResolutionClosed = errors.New("plugin: Service dependencies are only resolved during Plugin.Apply")
	// ErrWaterfallAlreadyExecuted reports a Middleware executing the same
	// downstream Action more than once during one call.
	ErrWaterfallAlreadyExecuted = errors.New("plugin: Waterfall Action already executed")
)

// Service marks a business capability offered through the Runtime. Provider
// interfaces embed Service and add their domain methods.
type Service interface {
	RuntimeService()
}

// Event is an owner-defined fact and its stable delivery contract.
type Event interface {
	EventName() string
	EventDelivery() DeliveryPolicy
}

// WaterfallInput marks an owner-defined interceptable operation input.
type WaterfallInput interface {
	RuntimeWaterfallInput()
}

// WaterfallInputBase supplies the WaterfallInput marker by embedding.
type WaterfallInputBase struct{}

// RuntimeWaterfallInput implements WaterfallInput.
func (WaterfallInputBase) RuntimeWaterfallInput() {}

// WaterfallOutput marks an owner-defined interceptable operation output.
type WaterfallOutput interface {
	RuntimeWaterfallOutput()
}

// WaterfallOutputBase supplies the WaterfallOutput marker by embedding.
type WaterfallOutputBase struct{}

// RuntimeWaterfallOutput implements WaterfallOutput.
func (WaterfallOutputBase) RuntimeWaterfallOutput() {}

// Base is the opaque activation anchor embedded by every Plugin. It does not
// expose Scope, registries, or lifecycle mutation to business methods.
type Base struct {
	mutex      sync.RWMutex
	activation *activation
}

// RuntimePlugin returns the embedded activation anchor.
func (pluginBase *Base) RuntimePlugin() *Base {
	return pluginBase
}

// RuntimeService allows a business interface embedding Service to be
// implemented directly by a Plugin that embeds Base.
func (*Base) RuntimeService() {}

func (pluginBase *Base) attach(selectedActivation *activation) error {
	if pluginBase == nil {
		return errors.New("plugin: Plugin returned a nil Base")
	}
	pluginBase.mutex.Lock()
	defer pluginBase.mutex.Unlock()
	if pluginBase.activation != nil {
		return errors.New("plugin: Plugin instance is already mounted")
	}
	pluginBase.activation = selectedActivation
	return nil
}

func (pluginBase *Base) detach(selectedActivation *activation) {
	if pluginBase == nil {
		return
	}
	pluginBase.mutex.Lock()
	if pluginBase.activation == selectedActivation {
		pluginBase.activation = nil
	}
	pluginBase.mutex.Unlock()
}

func (pluginBase *Base) currentActivation() *activation {
	if pluginBase == nil {
		return nil
	}
	pluginBase.mutex.RLock()
	selectedActivation := pluginBase.activation
	pluginBase.mutex.RUnlock()
	return selectedActivation
}

// Plugin is one statically linked runtime participant. Apply acquires the
// Plugin's own resources after Runtime has resolved its declared dependencies.
// Dispose must be idempotent and tolerate a partially completed Apply.
type Plugin interface {
	RuntimePlugin() *Base
	Manifest() Manifest
	Apply(context.Context) error
	Dispose(context.Context) error
}

// ServiceType is a type-derived Service contract used only by Manifest.
type ServiceType interface {
	Name() string
	serviceReference() serviceRef
	bindService(Plugin) (Service, bool)
}

// EventSubscription declares one Event type routed to the Plugin's unified
// EventObserver entry point.
type EventSubscription interface {
	Name() string
	eventReference() (eventRef, error)
	bindEventObserver(Plugin) (EventObserver, error)
}

// WaterfallContribution declares one Middleware owned by the Plugin for one
// typed operation.
type WaterfallContribution interface {
	Name() string
	waterfallReference() (waterfallRef, error)
	waterfallMiddleware() (waterfallInvoker, error)
}

// Manifest is the complete declarative contribution and dependency contract
// of one Plugin instance.
type Manifest struct {
	Name       string
	Provides   []ServiceType
	Requires   []ServiceType
	Optional   []ServiceType
	Events     []EventSubscription
	Waterfalls []WaterfallContribution
}

type serviceRef struct {
	key  reflect.Type
	name string
}

func (reference serviceRef) validate() error {
	if reference.key == nil || reference.key.Kind() != reflect.Interface ||
		reference.key.Name() == "" || reference.name == "" {
		return errors.New("Service contract must be a named interface")
	}
	return nil
}

type eventRef struct {
	key    reflect.Type
	name   string
	policy DeliveryPolicy
}

type waterfallRef struct {
	input  reflect.Type
	output reflect.Type
	name   string
}

type serviceOffer struct {
	reference  serviceRef
	capability Service
}

type eventOffer struct {
	reference eventRef
	observer  EventObserver
}

type waterfallOffer struct {
	reference waterfallRef
	invoker   waterfallInvoker
}

type manifestSpec struct {
	name       string
	provides   []serviceOffer
	requires   []serviceRef
	optional   []serviceRef
	events     []eventOffer
	waterfalls []waterfallOffer
}

func normalizeManifest(pluginInstance Plugin) (manifestSpec, error) {
	if pluginInstance == nil {
		return manifestSpec{}, errors.New("plugin: cannot mount nil Plugin")
	}
	if pluginInstance.RuntimePlugin() == nil {
		return manifestSpec{}, errors.New("plugin: Plugin returned a nil Base")
	}
	metadata := pluginInstance.Manifest()
	if strings.TrimSpace(metadata.Name) == "" || metadata.Name != strings.TrimSpace(metadata.Name) {
		return manifestSpec{}, errors.New("plugin: manifest name must be non-empty and trimmed")
	}
	normalized := manifestSpec{
		name: metadata.Name,
	}
	seenServices := make(map[reflect.Type]string)
	for _, declaredService := range metadata.Provides {
		reference, err := normalizeServiceType(
			metadata.Name,
			"provides",
			declaredService,
			seenServices,
		)
		if err != nil {
			return manifestSpec{}, err
		}
		capability, matches := declaredService.bindService(pluginInstance)
		if !matches || capability == nil {
			return manifestSpec{}, fmt.Errorf(
				"plugin: %s declares provided Service %q but does not implement it",
				metadata.Name,
				reference.name,
			)
		}
		normalized.provides = append(normalized.provides, serviceOffer{
			reference:  reference,
			capability: capability,
		})
	}
	for _, declaredService := range metadata.Requires {
		reference, err := normalizeServiceType(
			metadata.Name,
			"requires",
			declaredService,
			seenServices,
		)
		if err != nil {
			return manifestSpec{}, err
		}
		normalized.requires = append(normalized.requires, reference)
	}
	for _, declaredService := range metadata.Optional {
		reference, err := normalizeServiceType(
			metadata.Name,
			"optional",
			declaredService,
			seenServices,
		)
		if err != nil {
			return manifestSpec{}, err
		}
		normalized.optional = append(normalized.optional, reference)
	}

	seenEvents := make(map[reflect.Type]struct{})
	for _, declaredEvent := range metadata.Events {
		if declaredEvent == nil {
			return manifestSpec{}, fmt.Errorf(
				"plugin: %s declares an invalid Event subscription",
				metadata.Name,
			)
		}
		reference, err := declaredEvent.eventReference()
		if err != nil {
			return manifestSpec{}, fmt.Errorf(
				"plugin: %s Event subscription: %w",
				metadata.Name,
				err,
			)
		}
		if _, exists := seenEvents[reference.key]; exists {
			return manifestSpec{}, fmt.Errorf(
				"plugin: %s declares Event %q more than once",
				metadata.Name,
				reference.name,
			)
		}
		observer, err := declaredEvent.bindEventObserver(pluginInstance)
		if err != nil {
			return manifestSpec{}, fmt.Errorf(
				"plugin: %s Event %q: %w",
				metadata.Name,
				reference.name,
				err,
			)
		}
		seenEvents[reference.key] = struct{}{}
		normalized.events = append(normalized.events, eventOffer{
			reference: reference,
			observer:  observer,
		})
	}

	seenWaterfalls := make(map[waterfallKey]struct{})
	for _, declaredWaterfall := range metadata.Waterfalls {
		if declaredWaterfall == nil {
			return manifestSpec{}, fmt.Errorf(
				"plugin: %s declares an invalid Waterfall contribution",
				metadata.Name,
			)
		}
		reference, err := declaredWaterfall.waterfallReference()
		if err != nil {
			return manifestSpec{}, fmt.Errorf(
				"plugin: %s Waterfall contribution: %w",
				metadata.Name,
				err,
			)
		}
		selectedKey := waterfallKey{
			input:  reference.input,
			output: reference.output,
		}
		if _, exists := seenWaterfalls[selectedKey]; exists {
			return manifestSpec{}, fmt.Errorf(
				"plugin: %s declares Waterfall %q more than once",
				metadata.Name,
				reference.name,
			)
		}
		invoker, err := declaredWaterfall.waterfallMiddleware()
		if err != nil {
			return manifestSpec{}, fmt.Errorf(
				"plugin: %s Waterfall %q: %w",
				metadata.Name,
				reference.name,
				err,
			)
		}
		seenWaterfalls[selectedKey] = struct{}{}
		normalized.waterfalls = append(normalized.waterfalls, waterfallOffer{
			reference: reference,
			invoker:   invoker,
		})
	}
	return normalized, nil
}

func normalizeServiceType(
	pluginName string,
	groupName string,
	declaredService ServiceType,
	seen map[reflect.Type]string,
) (serviceRef, error) {
	if declaredService == nil {
		return serviceRef{}, fmt.Errorf(
			"plugin: %s %s: invalid Service type",
			pluginName,
			groupName,
		)
	}
	reference := declaredService.serviceReference()
	if err := reference.validate(); err != nil {
		return serviceRef{}, fmt.Errorf(
			"plugin: %s %s: %w",
			pluginName,
			groupName,
			err,
		)
	}
	if previousGroup, exists := seen[reference.key]; exists {
		return serviceRef{}, fmt.Errorf(
			"plugin: %s declares Service %q in both %s and %s",
			pluginName,
			reference.name,
			previousGroup,
			groupName,
		)
	}
	seen[reference.key] = groupName
	return reference, nil
}

func namedTypeName(selectedType reflect.Type) string {
	if selectedType == nil {
		return ""
	}
	if selectedType.PkgPath() == "" {
		return selectedType.String()
	}
	return selectedType.PkgPath() + "." + selectedType.Name()
}

func containsService(references []serviceRef, selectedKey reflect.Type) bool {
	for _, reference := range references {
		if reference.key == selectedKey {
			return true
		}
	}
	return false
}

func sameManifestContract(leftSpec manifestSpec, rightSpec manifestSpec) bool {
	if leftSpec.name != rightSpec.name ||
		!sameServiceSet(leftSpec.provides, rightSpec.provides) ||
		!sameServiceRefs(leftSpec.requires, rightSpec.requires) ||
		!sameServiceRefs(leftSpec.optional, rightSpec.optional) ||
		!sameEventSet(leftSpec.events, rightSpec.events) ||
		!sameWaterfallSet(leftSpec.waterfalls, rightSpec.waterfalls) {
		return false
	}
	return true
}

func sameServiceSet(leftOffers []serviceOffer, rightOffers []serviceOffer) bool {
	if len(leftOffers) != len(rightOffers) {
		return false
	}
	for _, leftOffer := range leftOffers {
		found := false
		for _, rightOffer := range rightOffers {
			if leftOffer.reference.key == rightOffer.reference.key {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func sameServiceRefs(leftRefs []serviceRef, rightRefs []serviceRef) bool {
	if len(leftRefs) != len(rightRefs) {
		return false
	}
	for _, leftRef := range leftRefs {
		if !containsService(rightRefs, leftRef.key) {
			return false
		}
	}
	return true
}

func sameEventSet(leftOffers []eventOffer, rightOffers []eventOffer) bool {
	if len(leftOffers) != len(rightOffers) {
		return false
	}
	for _, leftOffer := range leftOffers {
		found := false
		for _, rightOffer := range rightOffers {
			if leftOffer.reference.key == rightOffer.reference.key &&
				leftOffer.reference.name == rightOffer.reference.name &&
				leftOffer.reference.policy == rightOffer.reference.policy {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func sameWaterfallSet(leftOffers []waterfallOffer, rightOffers []waterfallOffer) bool {
	if len(leftOffers) != len(rightOffers) {
		return false
	}
	for _, leftOffer := range leftOffers {
		found := false
		for _, rightOffer := range rightOffers {
			if leftOffer.reference.input == rightOffer.reference.input &&
				leftOffer.reference.output == rightOffer.reference.output {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// FiberID identifies one concrete activation attempt.
type FiberID uint64

// FiberState is one externally observable lifecycle state.
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

// ScopeKey is an opaque comparable routing identity. Its zero value is root.
type ScopeKey struct {
	token *scopeToken
}

// IsGlobal reports whether this is the Runtime root Scope.
func (selectedKey ScopeKey) IsGlobal() bool {
	return selectedKey.token == nil
}

// ScopeLineage returns child Scope keys from the farthest ancestor to the
// selected key. The root Scope is omitted.
func ScopeLineage(selectedKey ScopeKey) []ScopeKey {
	tokens := make([]*scopeToken, 0)
	for selectedToken := selectedKey.token; selectedToken != nil; selectedToken = selectedToken.parent {
		tokens = append(tokens, selectedToken)
	}
	lineage := make([]ScopeKey, len(tokens))
	for tokenIndex := range tokens {
		lineage[len(tokens)-1-tokenIndex] = ScopeKey{
			token: tokens[tokenIndex],
		}
	}
	return lineage
}

// Handle identifies one mounted Plugin. Runtime owns unloading.
type Handle struct {
	owner *Runtime
	id    uint64
}

// ID returns the Runtime-local mount identity.
func (pluginHandle Handle) ID() uint64 {
	return pluginHandle.id
}

// FiberStatus is an immutable lifecycle and contribution diagnostic.
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

// ServiceDependencyStatus describes one resolved activation dependency.
type ServiceDependencyStatus struct {
	Service         string
	ProviderFiberID FiberID
	Optional        bool
}

// ServiceBindingStatus describes one provided Service.
type ServiceBindingStatus struct {
	Service string
	Scope   ScopeKey
}

// WaterfallBindingStatus describes one Middleware contribution.
type WaterfallBindingStatus struct {
	Waterfall string
	Scope     ScopeKey
}

// EventSubscriptionStatus describes one Observer contribution.
type EventSubscriptionStatus struct {
	Event string
	Scope ScopeKey
}
