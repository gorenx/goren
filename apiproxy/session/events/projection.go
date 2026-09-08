package sessionevents

import (
	"encoding/json"

	"github.com/gorenx/goren/session"
	sessionprojection "github.com/gorenx/goren/session/projection"
)

// ProjectProjections copies one domain projection snapshot into its
// wire block without exposing Registry state.
func ProjectProjections(source sessionprojection.Snapshot) *Projections {
	values := make(map[string]json.RawMessage, len(source.Values))
	for key, rawValue := range source.Values {
		values[key] = append(json.RawMessage(nil), rawValue...)
	}
	return &Projections{
		AsOfSeq: source.AsOfSeq,
		Values:  values,
	}
}

// ProjectEvent copies one committed Session fact into the wire event
// shape shared by history and live frames.
func ProjectEvent(committed session.Event) (LogEvent, error) {
	projected := LogEvent{
		Type:      committed.Type,
		Seq:       committed.Seq,
		Time:      committed.Time,
		Data:      append(json.RawMessage(nil), committed.Data...),
		Ignorable: committed.Ignorable,
	}
	if committed.SourceEventSeqs != nil {
		provenance := append([]int64(nil), (*committed.SourceEventSeqs)...)
		projected.SourceEventSeqs = &provenance
	}
	if committed.SurfaceOp != nil {
		encoded, err := json.Marshal(committed.SurfaceOp)
		if err != nil {
			return LogEvent{}, err
		}
		projected.SurfaceOp = encoded
	}
	return projected, nil
}

// Projections is one consistent whole-value projection cut.
type Projections struct {
	AsOfSeq int64                      `json:"asOfSeq"`
	Values  map[string]json.RawMessage `json:"values"`
}
