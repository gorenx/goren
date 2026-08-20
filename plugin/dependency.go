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
	runtime  *Runtime
	mounts   *mountTree
	services *serviceRegistry
}

func newDependencyGraph(
	runtimeEngine *Runtime,
	mounts *mountTree,
	services *serviceRegistry,
) *dependencyGraph {
	return &dependencyGraph{
		runtime:  runtimeEngine,
		mounts:   mounts,
		services: services,
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

func (graph *dependencyGraph) staleOptionalConsumers() []*fiber {
	graph.runtime.view.RLock()
	defer graph.runtime.view.RUnlock()
	stale := make([]*fiber, 0)
	for _, mounted := range graph.mounts.all() {
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

func (graph *dependencyGraph) directDependents(provider *fiber) []*fiber {
	dependents := make([]*fiber, 0)
	graph.runtime.view.RLock()
	defer graph.runtime.view.RUnlock()
	for _, mounted := range graph.mounts.all() {
		candidate := mounted.current
		if candidate == nil || candidate.state != FiberActive || candidate == provider {
			continue
		}
		for _, dependency := range candidate.dependencies {
			if dependency.binding == nil || dependency.binding.owner != provider {
				continue
			}
			dependents = append(dependents, candidate)
			break
		}
	}
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
