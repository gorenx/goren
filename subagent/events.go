package subagent

import (
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/session"
)

const (
	// ProviderAddedEventName is the vetoable Provider publication edge.
	ProviderAddedEventName = "subagent/provider-added"
	// ProviderRemovedEventName announces post-removal cleanup.
	ProviderRemovedEventName = "subagent/provider-removed"
	// StartEventName announces one accepted one-shot run or Activation epoch.
	StartEventName = "subagent/start"
	// EndEventName announces the paired terminal lifecycle edge.
	EndEventName = "subagent/end"
)

// RunID pairs one accepted start edge with exactly one terminal edge.
type RunID string

// ProviderAdded carries the exact newly registered Provider.
type ProviderAdded struct {
	Provider Provider
}

// EventName returns ProviderAddedEventName.
func (ProviderAdded) EventName() string {
	return ProviderAddedEventName
}

// EventDelivery preserves listener veto and registration rollback.
func (ProviderAdded) EventDelivery() plugin.DeliveryPolicy {
	return plugin.DeliveryOrdered
}

// ProviderRemoved carries the removed Provider name.
type ProviderRemoved struct {
	Name string
}

// EventName returns ProviderRemovedEventName.
func (ProviderRemoved) EventName() string {
	return ProviderRemovedEventName
}

// EventDelivery contains observer failures after removal commits.
func (ProviderRemoved) EventDelivery() plugin.DeliveryPolicy {
	return plugin.DeliveryBestEffort
}

// Started is observe-only identity for a published one-shot run or Activation
// residency epoch.
type Started struct {
	RunID    RunID
	Provider string
	ID       session.SessionID
	Local    bool
}

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
	LastAssistantMessage []llm.ContentBlock
}

// EventName returns EndEventName.
func (Ended) EventName() string {
	return EndEventName
}

// EventDelivery contains terminal observer failures after settlement.
func (Ended) EventDelivery() plugin.DeliveryPolicy {
	return plugin.DeliveryBestEffort
}
