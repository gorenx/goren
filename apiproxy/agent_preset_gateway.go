package apiproxy

import (
	"context"
	"errors"
	"fmt"
)

// AgentPreset is one detached preset supplied by a deployment roster.
type AgentPreset struct {
	ID          string
	Trust       AgentPresetTrust
	Name        *string
	Description *string
	Broken      *string
}

// AgentPresetRoster is the consumer-owned deployment roster capability.
type AgentPresetRoster interface {
	List(context.Context) ([]AgentPreset, error)
	DefaultID() string
	Authorable() bool
}

// AgentPresetGatewayOptions contains Host capabilities outside the roster.
type AgentPresetGatewayOptions struct {
	CanOpenPath bool
}

// AgentPresetGateway projects an optional deployment roster onto the Host API.
// A nil roster is a valid deployment: every Session uses the Host composition.
type AgentPresetGateway struct {
	roster      AgentPresetRoster
	canOpenPath bool
}

// NewAgentPresetGateway creates the roster projection for this deployment.
func NewAgentPresetGateway(roster AgentPresetRoster, settings AgentPresetGatewayOptions) *AgentPresetGateway {
	return &AgentPresetGateway{roster: roster, canOpenPath: settings.CanOpenPath}
}

// List returns an empty success when this deployment composes no presets.
func (owner *AgentPresetGateway) List(
	requestContext context.Context,
	_ Request[AgentPresetListRequest],
) (Outcome[AgentPresetListValue], error) {
	if owner.roster == nil {
		return OK(AgentPresetListValue{Presets: []AgentPresetEntry{}}), nil
	}
	presets, err := owner.roster.List(requestContext)
	if err != nil {
		return Outcome[AgentPresetListValue]{}, err
	}
	defaultID := owner.roster.DefaultID()
	entries := make([]AgentPresetEntry, 0, len(presets))
	seen := make(map[string]struct{}, len(presets))
	for _, preset := range presets {
		if err := validateAgentPreset(preset, seen); err != nil {
			return Outcome[AgentPresetListValue]{}, err
		}
		entries = append(entries, AgentPresetEntry{
			ID: preset.ID, Trust: preset.Trust, IsDefault: preset.ID == defaultID,
			Name: cloneStringPointer(preset.Name), Description: cloneStringPointer(preset.Description),
			Broken: cloneStringPointer(preset.Broken),
		})
	}
	return OK(AgentPresetListValue{
		Presets: entries, Authorable: owner.roster.Authorable(), HasDocument: owner.canOpenPath,
	}), nil
}

func validateAgentPreset(preset AgentPreset, seen map[string]struct{}) error {
	if preset.ID == "" {
		return errors.New("apiproxy: Agent Preset roster returned an empty id")
	}
	if preset.Trust != AgentPresetSystem && preset.Trust != AgentPresetUser {
		return fmt.Errorf("apiproxy: Agent Preset %q returned invalid trust %q", preset.ID, preset.Trust)
	}
	if preset.Broken != nil && *preset.Broken == "" {
		return fmt.Errorf("apiproxy: Agent Preset %q returned an empty broken reason", preset.ID)
	}
	if _, exists := seen[preset.ID]; exists {
		return fmt.Errorf("apiproxy: Agent Preset roster returned duplicate id %q", preset.ID)
	}
	seen[preset.ID] = struct{}{}
	return nil
}
