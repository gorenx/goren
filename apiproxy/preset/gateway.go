package presetapi

import (
	"context"
	"errors"
	"fmt"
	rpc "github.com/gorenx/goren/apiproxy/rpc"
)

// Preset is one detached preset supplied by a deployment roster.
type Preset struct {
	ID          string
	Trust       Trust
	Name        *string
	Description *string
	Broken      *string
}

// Roster is the consumer-owned deployment roster capability.
type Roster interface {
	List(context.Context) ([]Preset, error)
	DefaultID() string
	Authorable() bool
}

// Options contains Host capabilities outside the roster.
type Options struct {
	CanOpenPath bool
}

// Gateway projects an optional deployment roster onto the Host API.
// A nil roster is a valid deployment: every Session uses the Host composition.
type Gateway struct {
	roster      Roster
	canOpenPath bool
}

// NewGateway creates the roster projection for this deployment.
func NewGateway(source Roster, settings Options) *Gateway {
	return &Gateway{
		roster:      source,
		canOpenPath: settings.CanOpenPath,
	}
}

// List returns an empty success when this deployment composes no presets.
func (owner *Gateway) List(
	requestContext context.Context,
	_ rpc.Request[ListRequest],
) (rpc.Outcome[ListValue], error) {
	if owner.roster == nil {
		return rpc.OK(ListValue{
			Presets: []Entry{},
		}), nil
	}
	presets, err := owner.roster.List(requestContext)
	if err != nil {
		return rpc.Outcome[ListValue]{}, err
	}
	defaultID := owner.roster.DefaultID()
	entries := make([]Entry, 0, len(presets))
	seen := make(map[string]struct{}, len(presets))
	for _, preset := range presets {
		if err := validateAgentPreset(preset, seen); err != nil {
			return rpc.Outcome[ListValue]{}, err
		}
		entries = append(entries, Entry{
			ID:          preset.ID,
			Trust:       preset.Trust,
			IsDefault:   preset.ID == defaultID,
			Name:        cloneStringPointer(preset.Name),
			Description: cloneStringPointer(preset.Description),
			Broken:      cloneStringPointer(preset.Broken),
		})
	}
	return rpc.OK(ListValue{
		Presets:     entries,
		Authorable:  owner.roster.Authorable(),
		HasDocument: owner.canOpenPath,
	}), nil
}

func validateAgentPreset(entry Preset, seen map[string]struct{}) error {
	if entry.ID == "" {
		return errors.New("apiproxy: Agent Preset roster returned an empty id")
	}
	if entry.Trust != System && entry.Trust != User {
		return fmt.Errorf("apiproxy: Agent Preset %q returned invalid trust %q", entry.ID, entry.Trust)
	}
	if entry.Broken != nil && *entry.Broken == "" {
		return fmt.Errorf("apiproxy: Agent Preset %q returned an empty broken reason", entry.ID)
	}
	if _, exists := seen[entry.ID]; exists {
		return fmt.Errorf("apiproxy: Agent Preset roster returned duplicate id %q", entry.ID)
	}
	seen[entry.ID] = struct{}{}
	return nil
}

func cloneStringPointer(source *string) *string {
	if source == nil {
		return nil
	}
	detached := *source
	return &detached
}
