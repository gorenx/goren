package modelconfig

import (
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/session"
)

// Restore initializes missing Agent settings from its log or current defaults.
// Retained settings win when the same Session starts another Agent lifetime.
func (owner *Catalog) Restore(settings *llm.Configuration, agentID string, entries []session.Event) error {
	if _, err := settings.Get(agentID); err == nil {
		return nil
	}
	previous, err := session.LatestRequestHeader(entries)
	if err != nil {
		return err
	}
	proposed := llm.CallConfig{}
	if previous != nil {
		proposed = llm.CloneCallConfig(previous.Config)
		if previous.AdapterDefaults != nil {
			if previous.AdapterDefaults.ReasoningEffort {
				proposed.ReasoningEffort = ""
			}
			if previous.AdapterDefaults.MaxTokens {
				proposed.MaxTokens = nil
			}
		}
	}
	resolved, err := owner.Resolve(0, proposed)
	if err != nil {
		return err
	}
	return settings.Set(agentID, resolved.Config)
}
