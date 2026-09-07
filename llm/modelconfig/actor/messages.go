// Package configactor adapts model configuration requests to GoAkt.
package configactor

import (
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/llm/modelconfig"
)

// Initialize supplies validated initial business state from composition.
type Initialize struct {
	Catalog *modelconfig.Catalog
}

// Read requests the current configuration, or validates a specific version.
type Read struct {
	Version uint64
}

// Resolve requests a detached configuration for an Agent.
type Resolve struct {
	Version   uint64
	Overrides llm.CallConfig
}

// Replace publishes a complete definition if the expected version is current.
type Replace struct {
	ExpectedVersion uint64
	Definition      modelconfig.Definition
}

// Configuration preserves business errors without string conversion.
type Configuration struct {
	Snapshot modelconfig.Snapshot
	Err      error
}

// Resolution contains model parameters and a credential reference.
type Resolution struct {
	Resolved modelconfig.Resolved
	Err      error
}

// Completion acknowledges initialization.
type Completion struct {
	Err error
}
