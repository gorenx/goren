package title

import (
	"encoding/json"
	"errors"

	"github.com/gorenx/goren/session"
	sessionprojection "github.com/gorenx/goren/session/projection"
)

// Unit folds committed title events for the shared Projection owner.
type Unit struct{}

func (Unit) Key() string { return ProjectionKey }

func (Unit) StateVersion() int64 { return 1 }

func (Unit) InitialState() (json.RawMessage, error) {
	return json.RawMessage(`null`), nil
}

func (Unit) ApplyState(state json.RawMessage, committed session.Event) (sessionprojection.Transition, error) {
	if committed.Type != TitleEventName {
		return sessionprojection.Transition{State: state}, nil
	}
	var payload EventData
	if err := json.Unmarshal(committed.Data, &payload); err != nil {
		return sessionprojection.Transition{}, err
	}
	rawValue, err := json.Marshal(payload.Title)
	return sessionprojection.Transition{State: rawValue, Changed: true}, err
}

func (Unit) ViewState(state json.RawMessage) (json.RawMessage, error) {
	if string(state) == "null" {
		return state, nil
	}
	var title string
	if err := json.Unmarshal(state, &title); err != nil {
		return nil, err
	}
	if title == "" {
		return nil, errors.New("sessiontitle: title projection contains an empty title")
	}
	return json.Marshal(title)
}
