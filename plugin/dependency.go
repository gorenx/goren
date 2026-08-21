package plugin

import (
	"errors"
	"fmt"
	"reflect"
	"sort"
)

type dependencySnapshot map[reflect.Type]*serviceDependency

type dependencyResolution struct {
	selected dependencySnapshot
	missing  []serviceRef
	waiting  bool
}

func (resolution dependencyResolution) ready() bool {
	return !resolution.waiting && len(resolution.missing) == 0
}

func (resolution dependencyResolution) missingNames() []string {
	names := make([]string, 0, len(resolution.missing))
	for _, reference := range resolution.missing {
		names = append(names, reference.name)
	}
	sort.Strings(names)
	return names
}

// dependencyGraph owns cross-Fiber dependency queries. Runtime's view lock
// protects the Service registry and Fiber states it reads.
type dependencyGraph struct {
	runtime    *Runtime
	mounts     *mountTree
	services   *serviceRegistry
	dependents map[*fiber][]*fiber
}

func newDependencyGraph(
	runtimeEngine *Runtime,
	mounts *mountTree,
	services *serviceRegistry,
) *dependencyGraph {
	return &dependencyGraph{
		runtime:    runtimeEngine,
		mounts:     mounts,
		services:   services,
		dependents: make(map[*fiber][]*fiber),
	}
}

func (graph *dependencyGraph) resolve(
	mounted *pluginMount,
	target pluginTarget,
) dependencyResolution {
	resolution := dependencyResolution{
		selected: make(dependencySnapshot),
	}
	graph.runtime.view.RLock()
	defer graph.runtime.view.RUnlock()
	if mounted.parent != nil {
		if mounted.parent.removed || mounted.parent.current == nil ||
			mounted.parent.current.state != FiberActive {
			resolution.waiting = true
		}
	}
	for _, reference := range target.manifest.requires {
		binding, available := graph.services.resolve(reference, mounted.scope)
		if available {
			resolution.selected[reference.key] = &serviceDependency{
				reference: reference,
				binding:   binding,
			}
			continue
		}
		if graph.declaredProviderLocked(reference, mounted.scope, mounted) != nil {
			resolution.waiting = true
			continue
		}
		resolution.missing = append(resolution.missing, reference)
	}
	for _, reference := range target.manifest.optional {
		binding, available := graph.services.resolve(reference, mounted.scope)
		if !available {
			continue
		}
		resolution.selected[reference.key] = &serviceDependency{
			reference: reference,
			binding:   binding,
			optional:  true,
		}
	}
	return resolution
}

// Runtime.view must be read-locked by the caller.
func (graph *dependencyGraph) declaredProviderLocked(
	reference serviceRef,
	sourceScope *scope,
	excludedMount *pluginMount,
) *pluginMount {
	for selectedScope := sourceScope; selectedScope != nil; selectedScope = selectedScope.parent {
		for _, mounted := range graph.mounts.all() {
			if mounted == excludedMount || mounted.removed || mounted.scope != selectedScope {
				continue
			}
			for _, serviceDeclaration := range mounted.target.manifest.provides {
				if serviceDeclaration.reference.key == reference.key {
					return mounted
				}
			}
		}
	}
	return nil
}

func (graph *dependencyGraph) staleOptionalConsumers(
	mounts []*pluginMount,
) []*fiber {
	graph.runtime.view.RLock()
	defer graph.runtime.view.RUnlock()
	stale := make([]*fiber, 0)
	for _, mounted := range mounts {
		running := mounted.current
		if mounted.removed || running == nil || running.state != FiberActive {
			continue
		}
		for _, reference := range running.target.manifest.optional {
			resolved, _ := graph.services.resolve(reference, running.scope)
			current := running.dependencies[reference.key]
			if current == nil && resolved == nil {
				continue
			}
			if current != nil && current.binding == resolved {
				continue
			}
			stale = append(stale, running)
			break
		}
	}
	return stale
}

// addConsumer records the active dependency edges for one Fiber. The caller
// holds Runtime.view for the activation transaction.
func (graph *dependencyGraph) addConsumer(consumer *fiber) {
	var providerStorage [8]*fiber
	providers := uniqueDependencyProviders(
		consumer,
		providerStorage[:0],
	)
	for _, provider := range providers {
		graph.dependents[provider] = append(
			graph.dependents[provider],
			consumer,
		)
	}
}

// removeConsumer removes every active dependency edge for one Fiber. The
// caller holds Runtime.view for the stop transaction.
func (graph *dependencyGraph) removeConsumer(consumer *fiber) {
	var providerStorage [8]*fiber
	providers := uniqueDependencyProviders(
		consumer,
		providerStorage[:0],
	)
	for _, provider := range providers {
		candidates := graph.dependents[provider]
		for candidateIndex, candidate := range candidates {
			if candidate != consumer {
				continue
			}
			candidates = append(
				candidates[:candidateIndex],
				candidates[candidateIndex+1:]...,
			)
			if len(candidates) == 0 {
				delete(graph.dependents, provider)
			} else {
				graph.dependents[provider] = candidates
			}
			break
		}
	}
}

func uniqueDependencyProviders(
	consumer *fiber,
	providers []*fiber,
) []*fiber {
	for _, dependency := range consumer.dependencies {
		if dependency == nil || dependency.binding == nil ||
			dependency.binding.owner == nil {
			continue
		}
		provider := dependency.binding.owner
		duplicate := false
		for _, selected := range providers {
			if selected == provider {
				duplicate = true
				break
			}
		}
		if !duplicate {
			providers = append(providers, provider)
		}
	}
	return providers
}

func (graph *dependencyGraph) directDependents(provider *fiber) []*fiber {
	graph.runtime.view.RLock()
	dependents := append([]*fiber(nil), graph.dependents[provider]...)
	graph.runtime.view.RUnlock()
	sort.Slice(dependents, func(leftIndex int, rightIndex int) bool {
		return dependents[leftIndex].mount.handleID <
			dependents[rightIndex].mount.handleID
	})
	return dependents
}

func (graph *dependencyGraph) readiness(
	mounts []*pluginMount,
	phase ActivationPhase,
) error {
	graph.runtime.view.RLock()
	defer graph.runtime.view.RUnlock()
	var readinessErr error
	for _, mounted := range mounts {
		if mounted.removed || mounted.phase != phase || mounted.current == nil {
			continue
		}
		running := mounted.current
		if running.state == FiberActive {
			continue
		}
		switch running.state {
		case FiberWaiting:
			reason := "dependency cycle"
			if len(running.missing) != 0 {
				reason = fmt.Sprintf(
					"missing required Services: %v",
					running.missing,
				)
			}
			readinessErr = errors.Join(
				readinessErr,
				fmt.Errorf(
					"plugin: start %s: %s",
					running.target.manifest.name,
					reason,
				),
			)
		case FiberFailed:
			readinessErr = errors.Join(readinessErr, running.lastError)
		default:
			readinessErr = errors.Join(
				readinessErr,
				fmt.Errorf(
					"plugin: start %s stopped in state %s",
					running.target.manifest.name,
					running.state,
				),
			)
		}
	}
	return readinessErr
}
