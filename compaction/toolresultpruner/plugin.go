package toolresultpruner

import (
	"context"
	"errors"
	"fmt"
	"unicode/utf8"

	"github.com/gorenx/goren/llm/tokenmeter"
	"github.com/gorenx/goren/plugin"
)

const (
	defaultThresholdChars = 8192
	defaultHeadChars      = 4096
	defaultTailChars      = 1024
)

// Plugin owns Runtime publication and dependency binding for one Tool Result Pruner.
type Plugin struct {
	plugin.Base

	implementation ToolResultPruner
}

// ResolveConfig validates source-compatible pruning budgets.
func ResolveConfig(settings Config) (ResolvedConfig, error) {
	resolved := ResolvedConfig{
		ThresholdChars: defaultThresholdChars,
		HeadChars:      defaultHeadChars,
		TailChars:      defaultTailChars,
	}
	if settings.ThresholdChars != nil {
		resolved.ThresholdChars = *settings.ThresholdChars
	}
	if settings.HeadChars != nil {
		resolved.HeadChars = *settings.HeadChars
	}
	if settings.TailChars != nil {
		resolved.TailChars = *settings.TailChars
	}
	if resolved.ThresholdChars <= 0 || resolved.HeadChars < 0 || resolved.TailChars < 0 {
		return ResolvedConfig{}, errors.New(
			"toolresultpruner: threshold must be positive and head/tail non-negative",
		)
	}
	markerChars := utf8.RuneCountInString(PruneMarker)
	remaining := resolved.ThresholdChars
	if resolved.HeadChars > remaining {
		return ResolvedConfig{}, errors.New(
			"toolresultpruner: head + marker + tail exceed threshold",
		)
	}
	remaining -= resolved.HeadChars
	if markerChars > remaining {
		return ResolvedConfig{}, errors.New(
			"toolresultpruner: head + marker + tail exceed threshold",
		)
	}
	remaining -= markerChars
	if resolved.TailChars > remaining {
		return ResolvedConfig{}, fmt.Errorf(
			"toolresultpruner: head + marker + tail exceed threshold %d",
			resolved.ThresholdChars,
		)
	}
	return resolved, nil
}

// New constructs an inactive Tool Result Pruner Plugin.
func New(settings ResolvedConfig) *Plugin {
	return &Plugin{
		implementation: *newToolResultPruner(settings),
	}
}

// Manifest provides Pruner and requires the shared Meter.
func (owner *Plugin) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: PluginName,
		Provides: []plugin.ProvidedService{
			plugin.NewProvidedService[Pruner](&owner.implementation),
		},
		Requires: []plugin.ServiceType{
			plugin.ServiceOf[tokenmeter.Meter](),
		},
	}
}

// Apply resolves the singleton Meter dependency.
func (owner *Plugin) Apply(requestContext context.Context) error {
	if err := requestContext.Err(); err != nil {
		return err
	}
	meter, err := plugin.Require[tokenmeter.Meter](owner)
	if err != nil {
		return err
	}
	owner.implementation.bind(meter)
	return nil
}

// Dispose releases resolved capability snapshots.
func (owner *Plugin) Dispose(context.Context) error {
	owner.implementation.release()
	return nil
}
