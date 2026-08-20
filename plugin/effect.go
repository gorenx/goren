package plugin

import (
	"context"
	"errors"
	"fmt"
)

type effectState uint8

const (
	effectActive effectState = iota
	effectReleased
)

// effect is Runtime's private ownership record. Plugin authors neither create
// effects nor receive disposer handles.
type effect struct {
	label   string
	release func(context.Context) error
	state   effectState
}

type effectStack struct {
	entries []*effect
}

func (stack *effectStack) add(label string, releaseAction func(context.Context) error) {
	stack.entries = append(stack.entries, &effect{
		label:   label,
		release: releaseAction,
		state:   effectActive,
	})
}

func (stack *effectStack) labels() []string {
	effectLabels := make([]string, 0, len(stack.entries))
	for _, ownedEffect := range stack.entries {
		if ownedEffect.state == effectActive {
			effectLabels = append(effectLabels, ownedEffect.label)
		}
	}
	return effectLabels
}

func (stack *effectStack) release(releaseContext context.Context) error {
	var releaseErr error
	for effectIndex := len(stack.entries) - 1; effectIndex >= 0; effectIndex-- {
		releaseErr = errors.Join(
			releaseErr,
			releaseEffect(releaseContext, stack.entries[effectIndex]),
		)
	}
	stack.entries = nil
	return releaseErr
}

func releaseEffect(
	releaseContext context.Context,
	ownedEffect *effect,
) (releaseErr error) {
	if ownedEffect == nil || ownedEffect.state == effectReleased {
		return nil
	}
	ownedEffect.state = effectReleased
	defer func() {
		if recovered := recover(); recovered != nil {
			releaseErr = errors.Join(
				releaseErr,
				fmt.Errorf(
					"plugin: release effect %q panicked: %v",
					ownedEffect.label,
					recovered,
				),
			)
		}
	}()
	if ownedEffect.release == nil {
		return nil
	}
	return ownedEffect.release(releaseContext)
}
