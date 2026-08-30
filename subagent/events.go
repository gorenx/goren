package subagent

import (
	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/session"
)

const (
	// ProviderAddedEventName is the canonical vetoable SeedBuilder publication
	// event name.
	ProviderAddedEventName = "subagent/provider-added"
	// ProviderRemovedEventName announces post-removal cleanup.
	ProviderRemovedEventName = "subagent/provider-removed"
	// StartEventName announces one accepted OneShot or Continuable Execution.
	StartEventName = "subagent/start"
	// EndEventName announces the paired terminal lifecycle edge.
	EndEventName = "subagent/end"
)

// RunID pairs one accepted start edge with exactly one terminal edge.
type RunID string

// SeedBuilderAdded preserves the canonical event identity while carrying the
// exact newly registered SeedBuilder.
type SeedBuilderAdded struct {
	SeedBuilder SeedBuilder
}

// EventName returns ProviderAddedEventName.
func (SeedBuilderAdded) EventName() string {
	return ProviderAddedEventName
}

// EventDelivery preserves listener veto and registration rollback.
func (SeedBuilderAdded) EventDelivery() plugin.DeliveryPolicy {
	return plugin.DeliveryOrdered
}

// SeedBuilderRemoved carries the removed SeedBuilder name while preserving the
// canonical event identity.
type SeedBuilderRemoved struct {
	Name string
}

// EventName returns ProviderRemovedEventName.
func (SeedBuilderRemoved) EventName() string {
	return ProviderRemovedEventName
}

// EventDelivery contains observer failures after removal commits.
func (SeedBuilderRemoved) EventDelivery() plugin.DeliveryPolicy {
	return plugin.DeliveryBestEffort
}

// Started is observe-only identity for one published Subagent Execution.
type Started struct {
	RunID    RunID
	Provider string
	ID       session.SessionID
	Local    bool
}

func (Started) AgentScopedEvent() {}

// EventName returns StartEventName.
func (Started) EventName() string {
	return StartEventName
}

// EventDelivery contains lifecycle observer failures after publication.
func (Started) EventDelivery() plugin.DeliveryPolicy {
	return plugin.DeliveryBestEffort
}

// Ended is the terminal edge paired with Started by RunID.
type Ended struct {
	RunID                RunID
	Provider             string
	ID                   session.SessionID
	Local                bool
	StopReason           StopReason
	LastAssistantMessage []agentmessage.ContentBlock
}

func (Ended) AgentScopedEvent()  {}
func (Ended) AgentClosingEvent() {}

// EventName returns EndEventName.
func (Ended) EventName() string {
	return EndEventName
}

// EventDelivery contains terminal observer failures after settlement.
func (Ended) EventDelivery() plugin.DeliveryPolicy {
	return plugin.DeliveryBestEffort
}
