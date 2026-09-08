package basic

import (
	"errors"
	"fmt"
)

// Diagnostics suppresses repeated missing-capacity warnings for one Agent lifetime.
// Its caller serializes access and decides where to publish returned errors.
type Diagnostics struct {
	// targets contains exact provider/model keys whose pressure warning was reported.
	targets map[string]struct{}
}

// Pressure returns a reportable failure, or nil when absent or already reported.
// This method does not grant permission to continue an unfinished invocation.
func (subject *Diagnostics) Pressure(failure error) error {
	if failure == nil {
		return nil
	}
	var capacity *TargetPressureConfigError
	if errors.As(failure, &capacity) {
		if _, found := subject.targets[capacity.TargetKey]; found {
			return nil
		}
		if subject.targets == nil {
			subject.targets = make(map[string]struct{})
		}
		subject.targets[capacity.TargetKey] = struct{}{}
	}
	return fmt.Errorf("compaction: pressure processing failed: %w", failure)
}
