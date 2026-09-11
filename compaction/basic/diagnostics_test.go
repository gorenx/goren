package basic

import (
	"errors"
	"testing"
)

func TestPressureDiagnosticsDeduplicateOnlyCapacityTargets(t *testing.T) {
	owner := &Diagnostics{}
	missing := &TargetPressureConfigError{
		TargetKey: "p/m",
		Message:   "missing capacity",
	}
	if err := owner.Pressure(missing); !errors.Is(err, missing) {
		t.Fatal(err)
	}
	if err := owner.Pressure(missing); err != nil {
		t.Fatal("duplicate target warning", err)
	}
	another := &TargetPressureConfigError{
		TargetKey: "p/other",
		Message:   "missing capacity",
	}
	if err := owner.Pressure(another); !errors.Is(err, another) {
		t.Fatal(err)
	}
	failure := errors.New("summary failed")
	for range 2 {
		if err := owner.Pressure(failure); !errors.Is(err, failure) {
			t.Fatal("ordinary failure suppressed", err)
		}
	}
	if err := (&Diagnostics{}).Pressure(missing); !errors.Is(err, missing) {
		t.Fatal("another Agent inherited warnings", err)
	}
}
