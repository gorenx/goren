package plugin

import (
	"context"
	"errors"
	"fmt"
)

type replacementItem struct {
	mounted   *pluginMount
	declared  *pluginDeclaration
	previous  *fiber
	candidate *fiber
	prepared  bool
	published activationBindings
}

// subtreeReplacement owns one contract-preserving replacement command. It
// prepares candidate Main Fibers against a private Service view before any
// active binding changes.
type subtreeReplacement struct {
	runtime   *Runtime
	items     []*replacementItem
	byMount   map[*pluginMount]*replacementItem
	shadow    map[serviceBindingKey]*serviceBinding
	oldFibers map[*fiber]struct{}
	newFibers map[*fiber]struct{}
	oldOrder  []*fiber
}

func newSubtreeReplacement(
	runtimeEngine *Runtime,
	mounted *pluginMount,
	declaration *pluginDeclaration,
) (*subtreeReplacement, error) {
	command := &subtreeReplacement{
		runtime:   runtimeEngine,
		byMount:   make(map[*pluginMount]*replacementItem),
		shadow:    make(map[serviceBindingKey]*serviceBinding),
		oldFibers: make(map[*fiber]struct{}),
		newFibers: make(map[*fiber]struct{}),
	}
	if err := command.compareAndCollect(mounted, declaration, true); err != nil {
		return nil, err
	}
	for _, item := range command.items {
		if err := runtimeEngine.bindings.validateAdmission(item.declared.target); err != nil {
			return nil, err
		}
		item.candidate = runtimeEngine.activations.newFiberCandidate(
			item.mounted,
			item.declared.target,
		)
		command.oldFibers[item.previous] = struct{}{}
		command.newFibers[item.candidate] = struct{}{}
	}
	command.oldOrder = replacementStopOrder(command.items, false)
	return command, nil
}

func (command *subtreeReplacement) compareAndCollect(
	mounted *pluginMount,
	declaration *pluginDeclaration,
	root bool,
) error {
	if mounted == nil || declaration == nil {
		return errors.New("plugin: replacement tree is incomplete")
	}
	if mounted.phase == ActivationCommit || declaration.phase == ActivationCommit {
		return errors.New("plugin: a subtree containing commit-phase Plugins cannot be replaced")
	}
	if mounted.phase != declaration.phase ||
		!sameManifestContract(mounted.target.manifest, declaration.target.manifest) {
		return fmt.Errorf(
			"plugin: replacement changes the mounted Plugin contract at %s",
			mounted.target.manifest.name,
		)
	}
	if !root && mounted.placement != declaration.placement {
		return fmt.Errorf(
			"plugin: replacement changes Scope placement at %s",
			mounted.target.manifest.name,
		)
	}
	if len(mounted.children) != len(declaration.children) {
		return fmt.Errorf(
			"plugin: replacement changes child topology at %s",
			mounted.target.manifest.name,
		)
	}
	if mounted.current == nil || mounted.current.state != FiberActive {
		return fmt.Errorf(
			"plugin: replacement target %s is not active",
			mounted.target.manifest.name,
		)
	}
	item := &replacementItem{
		mounted:  mounted,
		declared: declaration,
		previous: mounted.current,
	}
	command.items = append(command.items, item)
	command.byMount[mounted] = item
	for childIndex, childMount := range mounted.children {
		if err := command.compareAndCollect(
			childMount,
			declaration.children[childIndex],
			false,
		); err != nil {
			return err
		}
	}
	return nil
}

func (command *subtreeReplacement) execute(requestContext context.Context) error {
	if err := command.prepare(requestContext); err != nil {
		return err
	}
	dependentErr := command.stopExternalDependents(requestContext)
	drainErr := command.withdrawAndDrainPrevious(requestContext)
	publicationErr := command.publishCandidates()
	if publicationErr != nil {
		restoreErr := command.restorePrevious(requestContext, publicationErr)
		return errors.Join(
			dependentErr,
			drainErr,
			publicationErr,
			restoreErr,
		)
	}
	disposeErr := command.disposePrevious(requestContext)
	settleErr := command.runtime.reconcile(requestContext)
	return errors.Join(dependentErr, drainErr, disposeErr, settleErr)
}

func (command *subtreeReplacement) prepare(requestContext context.Context) error {
	remaining := len(command.items)
	for remaining > 0 {
		progress := false
		for _, item := range command.items {
			if item.prepared {
				continue
			}
			resolution := command.resolve(item, false)
			command.runtime.view.Lock()
			item.candidate.missing = resolution.missingNames()
			command.runtime.view.Unlock()
			if !resolution.ready() {
				continue
			}
			if err := item.candidate.prepare(requestContext, resolution); err != nil {
				rollbackErr := command.rollbackCandidates(requestContext, err)
				return errors.Join(err, rollbackErr)
			}
			item.prepared = true
			command.publishShadowServices(item)
			remaining--
			progress = true
		}
		if progress {
			continue
		}
		var readinessErr error
		for _, item := range command.items {
			if item.prepared {
				continue
			}
			resolution := command.resolve(item, false)
			reason := "dependency cycle"
			if missing := resolution.missingNames(); len(missing) != 0 {
				reason = fmt.Sprintf("missing required Services: %v", missing)
			}
			readinessErr = errors.Join(
				readinessErr,
				fmt.Errorf(
					"plugin: prepare replacement %s: %s",
					item.declared.target.manifest.name,
					reason,
				),
			)
		}
		rollbackErr := command.rollbackCandidates(requestContext, readinessErr)
		return errors.Join(readinessErr, rollbackErr)
	}
	return command.refreshOptionalDependencies(requestContext)
}

func (command *subtreeReplacement) resolve(
	item *replacementItem,
	includeAllOptional bool,
) dependencyResolution {
	resolution := dependencyResolution{}
	if item.mounted.parent != nil {
		if parentItem := command.byMount[item.mounted.parent]; parentItem != nil {
			if !parentItem.prepared {
				resolution.waiting = true
			}
		} else {
			command.runtime.view.RLock()
			parentActive := item.mounted.parent.current != nil &&
				item.mounted.parent.current.state == FiberActive
			command.runtime.view.RUnlock()
			if !parentActive {
				resolution.waiting = true
			}
		}
	}
	for _, reference := range item.declared.target.manifest.requires {
		binding, available, waiting := command.resolveService(
			reference,
			item.mounted.scope,
			item.mounted,
		)
		if available {
			resolution.add(reference, binding, false)
			continue
		}
		if waiting {
			resolution.waiting = true
			continue
		}
		resolution.missing = append(resolution.missing, reference)
	}
	for _, reference := range item.declared.target.manifest.optional {
		binding, available, waiting := command.resolveService(
			reference,
			item.mounted.scope,
			item.mounted,
		)
		if waiting && !includeAllOptional {
			continue
		}
		if available {
			resolution.add(reference, binding, true)
		}
	}
	return resolution
}

func (command *subtreeReplacement) resolveService(
	reference serviceRef,
	sourceScope *scope,
	excludedMount *pluginMount,
) (*serviceBinding, bool, bool) {
	command.runtime.view.RLock()
	defer command.runtime.view.RUnlock()
	for selectedScope := sourceScope; selectedScope != nil; selectedScope = selectedScope.parent {
		bindingKey := serviceBindingKey{
			scope:       selectedScope,
			serviceType: reference.key,
		}
		if binding := command.shadow[bindingKey]; binding != nil {
			return binding, true, false
		}
		if command.hasUnpreparedProvider(reference, selectedScope, excludedMount) {
			return nil, false, true
		}
		existing := command.runtime.bindings.services.bindings[bindingKey]
		if existing == nil || existing.owner == nil || existing.owner.state != FiberActive {
			continue
		}
		if _, replaced := command.oldFibers[existing.owner]; replaced {
			continue
		}
		return existing, true, false
	}
	return nil, false, false
}

func (command *subtreeReplacement) hasUnpreparedProvider(
	reference serviceRef,
	selectedScope *scope,
	excludedMount *pluginMount,
) bool {
	for _, candidateItem := range command.items {
		if candidateItem.mounted == excludedMount || candidateItem.prepared ||
			candidateItem.mounted.scope != selectedScope {
			continue
		}
		for _, provided := range candidateItem.declared.target.manifest.provides {
			if provided.reference.key == reference.key {
				return true
			}
		}
	}
	return false
}

func (command *subtreeReplacement) publishShadowServices(item *replacementItem) {
	for _, provided := range item.declared.target.manifest.provides {
		bindingKey := serviceBindingKey{
			scope:       item.mounted.scope,
			serviceType: provided.reference.key,
		}
		command.shadow[bindingKey] = &serviceBinding{
			reference:  provided.reference,
			capability: provided.capability,
			owner:      item.candidate,
			scope:      item.mounted.scope,
		}
	}
}

func (command *subtreeReplacement) refreshOptionalDependencies(
	requestContext context.Context,
) error {
	for _, item := range command.items {
		finalResolution := command.resolve(item, true)
		if !optionalDependenciesChanged(
			item.candidate.dependencies,
			finalResolution.selected,
			item.declared.target.manifest.optional,
		) {
			continue
		}
		refreshCause := errors.New("plugin: refresh replacement optional dependencies")
		if err := item.candidate.rollback(requestContext, refreshCause); err != nil {
			rollbackErr := command.rollbackCandidates(requestContext, err)
			return errors.Join(err, rollbackErr)
		}
		item.candidate.calls = newFiberCallGate()
		if err := item.candidate.prepare(requestContext, finalResolution); err != nil {
			rollbackErr := command.rollbackCandidates(requestContext, err)
			return errors.Join(err, rollbackErr)
		}
		command.publishShadowServices(item)
	}
	return nil
}

func optionalDependenciesChanged(
	current dependencySnapshot,
	final dependencySnapshot,
	optional []serviceRef,
) bool {
	for _, reference := range optional {
		currentDependency := current[reference.key]
		finalDependency := final[reference.key]
		if currentDependency == nil || finalDependency == nil {
			if currentDependency != finalDependency {
				return true
			}
			continue
		}
		if currentDependency.binding.owner != finalDependency.binding.owner {
			return true
		}
	}
	return false
}

func (command *subtreeReplacement) stopExternalDependents(
	requestContext context.Context,
) error {
	visited := make(map[*fiber]struct{}, len(command.oldFibers))
	for previous := range command.oldFibers {
		visited[previous] = struct{}{}
	}
	var stopErr error
	for _, item := range command.items {
		orderedDependents := stopFiberOrder(
			command.runtime.dependencies.directDependents(item.previous),
		)
		for _, dependent := range orderedDependents {
			if _, internal := command.oldFibers[dependent]; internal {
				continue
			}
			stopErr = errors.Join(
				stopErr,
				command.runtime.activations.stopFiberWithDependents(
					requestContext,
					dependent,
					visited,
				),
			)
		}
	}
	return stopErr
}

func (command *subtreeReplacement) withdrawAndDrainPrevious(
	requestContext context.Context,
) error {
	command.runtime.view.Lock()
	for _, item := range command.items {
		item.previous.state = FiberStopping
		command.runtime.bindings.withdraw(item.previous.bindings)
		item.previous.calls.close()
	}
	command.runtime.view.Unlock()
	var drainErr error
	for _, item := range command.items {
		if err := item.previous.calls.wait(requestContext); err != nil {
			drainErr = errors.Join(drainErr, err)
			_ = item.previous.calls.wait(context.Background())
		}
	}
	return drainErr
}

func (command *subtreeReplacement) publishCandidates() error {
	command.runtime.view.Lock()
	defer command.runtime.view.Unlock()
	for _, item := range command.items {
		published, err := command.runtime.bindings.publish(item.candidate)
		if err != nil {
			for _, publishedItem := range command.items {
				command.runtime.bindings.withdraw(publishedItem.published)
			}
			return err
		}
		item.published = published
	}
	command.remapCandidateDependencies()
	for _, item := range command.items {
		item.mounted.target = item.declared.target
		item.mounted.current = item.candidate
		item.candidate.activate(item.published)
		command.runtime.dependencies.addConsumer(item.candidate)
	}
	return nil
}

// Runtime.view must be write-locked by the caller.
func (command *subtreeReplacement) remapCandidateDependencies() {
	for _, item := range command.items {
		for _, dependency := range item.candidate.dependencies {
			if dependency == nil || dependency.binding == nil {
				continue
			}
			if _, candidateOwned := command.newFibers[dependency.binding.owner]; !candidateOwned {
				continue
			}
			providerScope := dependency.binding.owner.scope
			bindingKey := serviceBindingKey{
				scope:       providerScope,
				serviceType: dependency.reference.key,
			}
			if published := command.runtime.bindings.services.bindings[bindingKey]; published != nil {
				dependency.binding = published
			}
		}
	}
}

func (command *subtreeReplacement) restorePrevious(
	requestContext context.Context,
	failure error,
) error {
	command.runtime.view.Lock()
	for _, item := range command.items {
		command.runtime.bindings.withdraw(item.published)
		command.runtime.bindings.restore(item.previous.bindings)
		item.previous.calls.open()
		item.previous.state = FiberActive
	}
	command.runtime.view.Unlock()
	rollbackErr := command.rollbackCandidates(requestContext, failure)
	settleErr := command.runtime.reconcile(requestContext)
	return errors.Join(rollbackErr, settleErr)
}

func (command *subtreeReplacement) disposePrevious(
	requestContext context.Context,
) error {
	var disposeErr error
	for _, previous := range command.oldOrder {
		disposeErr = errors.Join(
			disposeErr,
			previous.stop(requestContext),
		)
	}
	return disposeErr
}

func (command *subtreeReplacement) rollbackCandidates(
	requestContext context.Context,
	failure error,
) error {
	var rollbackErr error
	for _, candidate := range replacementStopOrder(command.items, true) {
		if !candidate.attached {
			continue
		}
		rollbackErr = errors.Join(
			rollbackErr,
			candidate.rollback(requestContext, failure),
		)
	}
	return rollbackErr
}

func replacementStopOrder(
	items []*replacementItem,
	candidates bool,
) []*fiber {
	selected := make(map[*fiber]struct{}, len(items))
	byMount := make(map[*pluginMount]*fiber, len(items))
	for _, item := range items {
		running := item.previous
		if candidates {
			running = item.candidate
		}
		if running == nil {
			continue
		}
		selected[running] = struct{}{}
		byMount[item.mounted] = running
	}
	ordered := make([]*fiber, 0, len(selected))
	visited := make(map[*fiber]struct{}, len(selected))
	var visit func(*fiber)
	visit = func(running *fiber) {
		if running == nil {
			return
		}
		if _, done := visited[running]; done {
			return
		}
		visited[running] = struct{}{}
		for dependent := range selected {
			if dependent == running {
				continue
			}
			for _, dependency := range dependent.dependencies {
				if dependency != nil && dependency.binding != nil &&
					dependency.binding.owner == running {
					visit(dependent)
					break
				}
			}
		}
		for _, childMount := range stopMountOrder(running.mount.children) {
			visit(byMount[childMount])
		}
		ordered = append(ordered, running)
	}
	for itemIndex := len(items) - 1; itemIndex >= 0; itemIndex-- {
		item := items[itemIndex]
		running := item.previous
		if candidates {
			running = item.candidate
		}
		visit(running)
	}
	return ordered
}
