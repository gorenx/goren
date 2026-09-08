package tokenmeter

import "github.com/gorenx/goren/session/projection"

// Units returns the domain-owned computations to register with the projection owner.
func Units() []projection.Unit {
	return []projection.Unit{
		tokenUsageUnit{},
		contextPressureUnit{},
		contextBreakdownUnit{},
	}
}
