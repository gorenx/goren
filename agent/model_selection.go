package agent

import (
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/session"
)

// ModelSelection is one complete provider/model choice, independent of an
// in-flight request or the actor that owns the explicit selection.
type ModelSelection struct {
	Provider        string
	Model           string
	ReasoningEffort llm.ReasoningEffortID
}

// ResolveModelSelection prefers an explicit choice, then the latest recorded
// request, then deployment defaults. Inputs are already acquired snapshots;
// this function performs no I/O or provider capability validation. Nil means
// no selection is available. A returned value never aliases requested.
func ResolveModelSelection(requested *ModelSelection, entries []session.Event, defaults ModelSelection) (*ModelSelection, error) {
	if requested != nil {
		chosen := *requested
		return &chosen, nil
	}
	header, err := session.LatestRequestHeader(entries)
	if err != nil {
		return nil, err
	}
	if header != nil {
		return &ModelSelection{
			Provider:        header.Config.Provider,
			Model:           header.Config.Model,
			ReasoningEffort: header.Config.ReasoningEffort,
		}, nil
	}
	if defaults.Provider == "" || defaults.Model == "" {
		return nil, nil
	}
	return &defaults, nil
}
