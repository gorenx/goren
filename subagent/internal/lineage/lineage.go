// Package lineage derives the immutable parent-to-child creation facts shared
// by one-shot Runs and continuable Activations.
package lineage

import (
	"errors"
	"fmt"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/session"
)

const maxSafeInteger int64 = 1<<53 - 1

// Lineage is one resolved child's inherited Session metadata and depth.
type Lineage struct {
	parentID session.SessionID
	header   session.Header
	options  agent.Options
	depth    int64
}

// From resolves one direct child lineage and enforces the caller's depth cap.
func From(parentAgent agent.Agent, maxDepth *int64) (Lineage, error) {
	if parentAgent == nil || parentAgent.SessionValue() == nil {
		return Lineage{}, errors.New(
			"subagent: child lineage requires a parent Agent and Session",
		)
	}
	if maxDepth != nil && (*maxDepth < 0 || *maxDepth > maxSafeInteger) {
		return Lineage{}, errors.New(
			"subagent: maxDepth must be a non-negative safe integer",
		)
	}
	parentDepth := int64(0)
	parentOptions := parentAgent.OptionsValue()
	if parentOptions.SubagentDepth != nil {
		parentDepth = *parentOptions.SubagentDepth
	}
	parentHeader := parentAgent.SessionValue().Header()
	if parentHeader.DelegationDepth != nil &&
		*parentHeader.DelegationDepth > parentDepth {
		parentDepth = *parentHeader.DelegationDepth
	}
	if parentDepth >= maxSafeInteger {
		return Lineage{}, errors.New(
			"subagent: child depth exceeds the safe integer range",
		)
	}
	childDepth := parentDepth + 1
	if maxDepth != nil && childDepth > *maxDepth {
		return Lineage{}, fmt.Errorf(
			"subagent: child depth %d exceeds maxDepth %d",
			childDepth,
			*maxDepth,
		)
	}
	return Lineage{
		parentID: parentAgent.ID(),
		header:   parentHeader,
		options:  parentOptions,
		depth:    childDepth,
	}, nil
}

// AgentOptions overlays explicit child settings onto the exact parent defaults
// while stamping the resolved lineage depth.
func (value Lineage) AgentOptions(requested *agent.Options) agent.Options {
	resolved := value.options
	resolved.MaxTokens = intPointer(value.options.MaxTokens)
	if requested != nil {
		if requested.Provider != "" {
			resolved.Provider = requested.Provider
		}
		if requested.Model != "" {
			resolved.Model = requested.Model
		}
		if requested.MaxTokens != nil {
			resolved.MaxTokens = intPointer(requested.MaxTokens)
		}
	}
	resolved.SubagentDepth = int64Pointer(value.depth)
	return resolved
}

// Metadata returns fresh Session metadata for one child creation.
func (value Lineage) Metadata(seedLength int64) session.Metadata {
	sessionMetadata := session.Metadata{
		CWD:             stringPointer(value.header.CWD),
		ParentSession:   sessionIDPointer(value.parentID),
		Origin:          session.OriginSubagent,
		DelegationDepth: int64Pointer(value.depth),
		AgentPreset:     stringPointer(value.header.AgentPreset),
	}
	if seedLength > 0 {
		sessionMetadata.SeedLength = int64Pointer(seedLength)
	}
	return sessionMetadata
}

func intPointer(source *int) *int {
	if source == nil {
		return nil
	}
	value := *source
	return &value
}

func int64Pointer(value int64) *int64 {
	return &value
}

func stringPointer(source *string) *string {
	if source == nil {
		return nil
	}
	value := *source
	return &value
}

func sessionIDPointer(value session.SessionID) *session.SessionID {
	return &value
}
