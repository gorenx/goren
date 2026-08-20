package plugin

import (
	"context"
	"errors"
	"fmt"
)

type fiberEffectState uint8

const (
	fiberEffectStaged fiberEffectState = iota
	fiberEffectActive
	fiberEffectWithdrawn
	fiberEffectDisposed
)

// fiberEffect is Runtime's private ownership record for one reversible side
// effect of a Plugin activation. Plugin authors never create or dispose it.
type fiberEffect struct {
	runtime      *Runtime
	fiber        *fiber
	scope        *Scope
	label        string
	release      func(context.Context) error
	registration runtimeEntry
	state        fiberEffectState
}

type fiberEffectStack struct {
	entries []*fiberEffect
}

func newFiberEffectStack() *fiberEffectStack {
	return &fiberEffectStack{}
}

func (stack *fiberEffectStack) labels() []string {
	effectLabels := make([]string, 0, len(stack.entries))
	for _, ownedEffect := range stack.entries {
		if ownedEffect.state == fiberEffectActive {
			effectLabels = append(effectLabels, ownedEffect.label)
		}
	}
	return effectLabels
}

func (ownedEffect *fiberEffect) withdrawRegistration(context.Context) error {
	if ownedEffect == nil || ownedEffect.registration == nil ||
		ownedEffect.state != fiberEffectActive {
		return nil
	}
	ownedEffect.runtime.state.Lock()
	if ownedEffect.state == fiberEffectActive {
		ownedEffect.registration.withdrawEntry(ownedEffect)
		ownedEffect.state = fiberEffectWithdrawn
	}
	ownedEffect.runtime.state.Unlock()
	return nil
}

func releaseFiberEffects(releaseContext context.Context, entries []*fiberEffect) error {
	var releaseErr error
	for entryIndex := len(entries) - 1; entryIndex >= 0; entryIndex-- {
		releaseErr = errors.Join(
			releaseErr,
			releaseFiberEffect(releaseContext, entries[entryIndex]),
		)
	}
	return releaseErr
}

func releaseFiberEffect(
	releaseContext context.Context,
	ownedEffect *fiberEffect,
) (releaseErr error) {
	if ownedEffect == nil || ownedEffect.state == fiberEffectDisposed {
		return nil
	}
	defer func() {
		ownedEffect.state = fiberEffectDisposed
		if recovered := recover(); recovered != nil {
			releaseErr = errors.Join(
				releaseErr,
				fmt.Errorf("plugin: release effect %q panicked: %v", ownedEffect.label, recovered),
			)
		}
	}()
	if ownedEffect.release == nil {
		return nil
	}
	return ownedEffect.release(releaseContext)
}
