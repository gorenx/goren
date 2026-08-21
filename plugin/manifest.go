package plugin

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
)

var namedTypeNames sync.Map

// ChildPlacement controls the Scope relationship between a Plugin and one of
// its declaratively owned children.
type ChildPlacement uint8

const (
	// SameScope places a child in its parent's exact Scope.
	SameScope ChildPlacement = iota
	// NestedScope creates one child Scope below the parent Scope.
	NestedScope
)

// ActivationPhase controls when a declarative child becomes active.
type ActivationPhase uint8

const (
	// ActivationMain participates in ordinary dependency-driven activation.
	ActivationMain ActivationPhase = iota
	// ActivationCommit starts only after every ordinary node in the admitted
	// tree is active. It is stopped before ordinary nodes.
	ActivationCommit
)

// ChildPlugin declares one Plugin instance owned by a composite Plugin.
// Runtime topology remains private to this package.
type ChildPlugin struct {
	Instance  Plugin
	Placement ChildPlacement
	Phase     ActivationPhase
}

// Manifest is the complete declarative binding, dependency, and owned-child
// contract of one Plugin instance. Runtime reads it exactly once while building
// a detached declaration tree, so implementations must return a stable snapshot
// without performing effects.
type Manifest struct {
	Name       string
	Provides   []ServiceType
	Requires   []ServiceType
	Optional   []ServiceType
	Events     []EventSubscription
	Waterfalls []WaterfallMiddlewareBinding
	Children   []ChildPlugin
}

type manifestSpec struct {
	name       string
	provides   []providedServiceSpec
	requires   []serviceRef
	optional   []serviceRef
	events     []eventSubscriptionSpec
	waterfalls []waterfallMiddlewareSpec
}

func normalizeManifest(
	pluginInstance Plugin,
	metadata Manifest,
) (manifestSpec, error) {
	if pluginInstance == nil {
		return manifestSpec{}, errors.New("plugin: cannot mount nil Plugin")
	}
	if strings.TrimSpace(metadata.Name) == "" || metadata.Name != strings.TrimSpace(metadata.Name) {
		return manifestSpec{}, errors.New("plugin: manifest name must be non-empty and trimmed")
	}
	normalized := manifestSpec{
		name: metadata.Name,
	}
	serviceCount := len(metadata.Provides) + len(metadata.Requires) + len(metadata.Optional)
	var seenServices map[reflect.Type]string
	if serviceCount != 0 {
		seenServices = make(map[reflect.Type]string, serviceCount)
	}
	if len(metadata.Provides) != 0 {
		normalized.provides = make(
			[]providedServiceSpec,
			0,
			len(metadata.Provides),
		)
	}
	if len(metadata.Requires) != 0 {
		normalized.requires = make([]serviceRef, 0, len(metadata.Requires))
	}
	if len(metadata.Optional) != 0 {
		normalized.optional = make([]serviceRef, 0, len(metadata.Optional))
	}
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
		normalized.provides = append(normalized.provides, providedServiceSpec{
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

	var seenEvents map[reflect.Type]struct{}
	if len(metadata.Events) != 0 {
		seenEvents = make(map[reflect.Type]struct{}, len(metadata.Events))
		normalized.events = make(
			[]eventSubscriptionSpec,
			0,
			len(metadata.Events),
		)
	}
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
		normalized.events = append(normalized.events, eventSubscriptionSpec{
			reference: reference,
			observer:  observer,
		})
	}

	var seenWaterfalls map[waterfallKey]struct{}
	if len(metadata.Waterfalls) != 0 {
		seenWaterfalls = make(map[waterfallKey]struct{}, len(metadata.Waterfalls))
		normalized.waterfalls = make(
			[]waterfallMiddlewareSpec,
			0,
			len(metadata.Waterfalls),
		)
	}
	for _, declaredWaterfall := range metadata.Waterfalls {
		if declaredWaterfall == nil {
			return manifestSpec{}, fmt.Errorf(
				"plugin: %s declares an invalid Waterfall Middleware binding",
				metadata.Name,
			)
		}
		reference, err := declaredWaterfall.waterfallReference()
		if err != nil {
			return manifestSpec{}, fmt.Errorf(
				"plugin: %s Waterfall Middleware binding: %w",
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
		normalized.waterfalls = append(normalized.waterfalls, waterfallMiddlewareSpec{
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
		if previousGroup == "provides" && groupName == "requires" {
			seen[reference.key] = groupName
			return reference, nil
		}
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
	if cached, found := namedTypeNames.Load(selectedType); found {
		return cached.(string)
	}
	typeName := selectedType.String()
	if selectedType.PkgPath() == "" {
		cached, _ := namedTypeNames.LoadOrStore(selectedType, typeName)
		return cached.(string)
	}
	typeName = selectedType.PkgPath() + "." + selectedType.Name()
	cached, _ := namedTypeNames.LoadOrStore(selectedType, typeName)
	return cached.(string)
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
	return leftSpec.name == rightSpec.name &&
		sameServiceSet(leftSpec.provides, rightSpec.provides) &&
		sameServiceRefs(leftSpec.requires, rightSpec.requires) &&
		sameServiceRefs(leftSpec.optional, rightSpec.optional) &&
		sameEventSet(leftSpec.events, rightSpec.events) &&
		sameWaterfallSet(leftSpec.waterfalls, rightSpec.waterfalls)
}

func sameServiceSet(leftOffers []providedServiceSpec, rightOffers []providedServiceSpec) bool {
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

func sameEventSet(leftOffers []eventSubscriptionSpec, rightOffers []eventSubscriptionSpec) bool {
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

func sameWaterfallSet(leftOffers []waterfallMiddlewareSpec, rightOffers []waterfallMiddlewareSpec) bool {
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
