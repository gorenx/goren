package toolresultpruner

import (
	"errors"
	"fmt"
	"unicode/utf8"
)

const (
	defaultThresholdChars = 8192
	defaultHeadChars      = 4096
	defaultTailChars      = 1024
)

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

// Clone detaches optional budgets before retaining a lifecycle configuration.
func (settings Config) Clone() Config {
	detached := settings
	if settings.ThresholdChars != nil {
		value := *settings.ThresholdChars
		detached.ThresholdChars = &value
	}
	if settings.HeadChars != nil {
		value := *settings.HeadChars
		detached.HeadChars = &value
	}
	if settings.TailChars != nil {
		value := *settings.TailChars
		detached.TailChars = &value
	}
	return detached
}
