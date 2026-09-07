package title

import (
	"errors"

	"github.com/gorenx/goren/session"
)

// ShouldGenerate checks an incoming human message against the committed snapshot.
// Automatic titles use their own configured model, independent of main requests.
func ShouldGenerate(metadata session.Header, events []session.Event, incoming session.Event, mode AutomaticMode) (bool, error) {
	if mode != AutomaticFirstPrompt && mode != AutomaticAllPrompts {
		return false, errors.New("sessiontitle: invalid automatic mode")
	}
	eligible, err := CollectMessages([]session.Event{incoming}, nil)
	if err != nil || len(eligible) == 0 {
		return false, err
	}
	accepted, err := Fold(events)
	if err != nil {
		return false, err
	}
	if accepted != nil && accepted.Source.SourceKind() == "user" {
		return false, nil
	}
	messages, err := CollectMessages(events, nil)
	if err != nil {
		return false, err
	}
	if len(messages) == 0 || messages[len(messages)-1].Seq != incoming.Seq {
		return false, nil
	}
	if mode == AutomaticAllPrompts {
		return true, nil
	}
	if metadata.ParentSession != nil || len(messages) != 1 {
		return false, nil
	}
	for index, entry := range events {
		if entry.Seq >= incoming.Seq {
			prior, err := Fold(events[:index])
			return prior == nil, err
		}
	}
	return false, nil
}
