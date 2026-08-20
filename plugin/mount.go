package plugin

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

type mountState uint8

const (
	mountOpen mountState = iota
	mountCommitting
	mountCommitted
	mountRolledBack
)

// mountTransaction owns the Plugin lifecycle and every registration created by
// one Plugin.Apply call. Registry effects remain hidden until commit; any
// failure releases the single Fiber effect stack in reverse order.
type mountTransaction struct {
	runtime      *Runtime
	fiber        *fiber
	applyContext context.Context
	effects      []*fiberEffect
	replacement  *replacementTransaction
	failure      error
	state        mountState
}

type replacementTransaction struct {
	previous  *fiber
	candidate *fiber
}

func newMountTransaction(
	runtimeEngine *Runtime,
	ownerFiber *fiber,
	applyContext context.Context,
) *mountTransaction {
	transaction := &mountTransaction{
		runtime:      runtimeEngine,
		fiber:        ownerFiber,
		applyContext: applyContext,
		state:        mountOpen,
	}
	transaction.effects = append(
		transaction.effects,
		&fiberEffect{
			runtime: runtimeEngine,
			fiber:   ownerFiber,
			scope:   ownerFiber.rootScope,
			label:   "plugin:" + ownerFiber.manifest.Name,
			release: ownerFiber.disposePlugin,
			state:   fiberEffectActive,
		},
	)
	return transaction
}

func (transaction *mountTransaction) stageEntry(
	ownerScope *Scope,
	entry runtimeEntry,
) error {
	if transaction == nil || transaction.state != mountOpen {
		return ErrRegistrationClosed
	}
	if ownerScope == nil || ownerScope.runtime != transaction.runtime ||
		ownerScope.ownerFiber != transaction.fiber || ownerScope.isClosed() {
		return transaction.recordFailure(ErrContextClosed)
	}
	if entry == nil {
		return transaction.recordFailure(errors.New("plugin: cannot stage nil Runtime entry"))
	}
	if _, isService := entry.(serviceBinding); isService && ownerScope != transaction.fiber.rootScope {
		return transaction.recordFailure(
			errors.New("plugin: Service providers must be installed at the Fiber root Scope"),
		)
	}
	rawLabel := entry.Label()
	effectLabel := strings.TrimSpace(rawLabel)
	if effectLabel == "" || effectLabel != rawLabel {
		return transaction.recordFailure(errors.New("plugin: registration label must be non-empty and trimmed"))
	}
	ownership := &fiberEffect{
		runtime:      transaction.runtime,
		fiber:        transaction.fiber,
		scope:        ownerScope,
		label:        effectLabel,
		registration: entry,
		state:        fiberEffectStaged,
	}
	ownership.release = ownership.withdrawRegistration
	transaction.effects = append(transaction.effects, ownership)
	return nil
}

func (transaction *mountTransaction) commit() error {
	if transaction == nil || transaction.state != mountOpen {
		return ErrRegistrationClosed
	}
	transaction.state = mountCommitting
	if transaction.failure != nil {
		return transaction.failCommit(transaction.failure)
	}
	if err := transaction.validateProvidedServices(); err != nil {
		return transaction.failCommit(err)
	}

	transaction.runtime.state.Lock()
	previousEffects := transaction.previousRegistrationEffects()
	withdrawRegistrationEffects(previousEffects)
	publishedEffects := make([]*fiberEffect, 0, len(transaction.effects))
	var publicationErr error
	for _, ownership := range transaction.effects {
		if ownership.registration == nil {
			continue
		}
		if err := ownership.registration.validateEntry(ownership); err != nil {
			publicationErr = err
			break
		}
		ownership.registration.publishEntry(ownership)
		ownership.state = fiberEffectActive
		publishedEffects = append(publishedEffects, ownership)
	}
	if publicationErr != nil {
		withdrawRegistrationEffects(publishedEffects)
		publishRegistrationEffects(previousEffects)
		transaction.runtime.state.Unlock()
		return transaction.failCommit(publicationErr)
	}
	transaction.runtime.state.Unlock()

	transaction.fiber.effects.entries = append(
		transaction.fiber.effects.entries,
		transaction.effects...,
	)
	transaction.state = mountCommitted
	if transaction.fiber.pluginContext != nil {
		transaction.fiber.pluginContext.transaction = nil
	}
	return nil
}

func (transaction *mountTransaction) rollback(disposeContext context.Context) error {
	if transaction == nil || transaction.state == mountRolledBack {
		return nil
	}
	if transaction.state == mountCommitted {
		return errors.New("plugin: cannot roll back a committed mount")
	}
	transaction.state = mountRolledBack
	return releaseFiberEffects(disposeContext, transaction.effects)
}

func (transaction *mountTransaction) recordFailure(operationErr error) error {
	transaction.failure = errors.Join(transaction.failure, operationErr)
	return operationErr
}

func (transaction *mountTransaction) failCommit(commitErr error) error {
	cleanupErr := releaseFiberEffects(transaction.applyContext, transaction.effects)
	transaction.state = mountRolledBack
	return errors.Join(commitErr, cleanupErr)
}

func (transaction *mountTransaction) previousRegistrationEffects() []*fiberEffect {
	if transaction.replacement == nil || transaction.replacement.previous == nil {
		return nil
	}
	previousEffects := transaction.replacement.previous.effects.entries
	registrations := make([]*fiberEffect, 0, len(previousEffects))
	for _, ownership := range previousEffects {
		if ownership.registration != nil {
			registrations = append(registrations, ownership)
		}
	}
	return registrations
}

func (transaction *mountTransaction) validateProvidedServices() error {
	for _, providedRef := range transaction.fiber.manifest.Provides {
		provided := false
		for _, ownership := range transaction.effects {
			binding, isService := ownership.registration.(serviceBinding)
			if isService && binding.bindingRef().sameDefinition(providedRef) {
				provided = true
				break
			}
		}
		if !provided {
			return fmt.Errorf(
				"plugin: %s declared Service %q but did not provide it",
				transaction.fiber.manifest.Name,
				providedRef.name,
			)
		}
	}
	return nil
}

// Runtime.state must be write-locked by the caller.
func withdrawRegistrationEffects(entries []*fiberEffect) {
	for entryIndex := len(entries) - 1; entryIndex >= 0; entryIndex-- {
		ownership := entries[entryIndex]
		if ownership.registration == nil || ownership.state != fiberEffectActive {
			continue
		}
		ownership.registration.withdrawEntry(ownership)
		ownership.state = fiberEffectWithdrawn
	}
}

// Runtime.state must be write-locked by the caller.
func publishRegistrationEffects(entries []*fiberEffect) {
	for _, ownership := range entries {
		if ownership.registration == nil || ownership.state == fiberEffectActive {
			continue
		}
		ownership.registration.publishEntry(ownership)
		ownership.state = fiberEffectActive
	}
}
