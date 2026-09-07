package title

import (
	"context"
	"errors"

	"github.com/gorenx/goren/session"
)

// ErrSuperseded rejects a result whose input title or human messages changed.
var ErrSuperseded = errors.New("sessiontitle: generation was superseded")

// Policy owns deterministic title validation and Session write intent.
type Policy struct {
	settings Config
}

// NewPolicy validates the title policy without acquiring runtime resources.
func NewPolicy(settings Config) (*Policy, error) {
	validated, err := settings.Validate()
	if err != nil {
		return nil, err
	}
	return &Policy{settings: validated}, nil
}

// Rename prepares a user title; committing it pins automatic generation.
func (rules *Policy) Rename(input string) (session.WritePlan, error) {
	normalized, err := NormalizeSessionTitle(input, rules.settings.MaxTitleBytes)
	if err != nil {
		return nil, err
	}
	if normalized == "" {
		return nil, &SessionTitleInvalidError{}
	}
	draft, err := session.NewEventDraft(TitleSet, EventData{
		Title:       normalized,
		MessageSeqs: []int64{},
		Source:      UserSource{Kind: "user"},
	})
	if err != nil {
		return nil, err
	}
	return session.Batch(draft), nil
}

// Fallback fills an absent title from the first eligible human message.
// Build rechecks the current log so a concurrent rename always wins.
func (rules *Policy) Fallback() session.WritePlan {
	return fallbackPlan{settings: rules.settings}
}

type fallbackPlan struct {
	settings Config
}

func (plan fallbackPlan) Build(_ context.Context, snapshot session.Snapshot) ([]session.EventDraft, error) {
	accepted, err := Fold(snapshot.Events)
	if err != nil || accepted != nil {
		return nil, err
	}
	messages, err := CollectMessages(snapshot.Events, nil)
	if err != nil || len(messages) == 0 {
		return nil, err
	}
	normalized, err := FallbackSessionTitle(messages[0].Text, plan.settings.FallbackMaxWords, plan.settings.FallbackMaxBytes)
	if err != nil || normalized == "" {
		return nil, err
	}
	draft, err := session.NewEventDraft(TitleSet, EventData{
		Title:       normalized,
		MessageSeqs: []int64{messages[0].Seq},
		Source:      FallbackSource{Kind: "fallback"},
	})
	if err != nil {
		return nil, err
	}
	return []session.EventDraft{draft}, nil
}

// RefreshFallback explicitly unpins a user title when no model call is available.
// Existing automatic titles and sessions without human text remain unchanged.
func (rules *Policy) RefreshFallback(entries []session.Event) (session.WritePlan, error) {
	accepted, err := Fold(entries)
	if err != nil {
		return nil, err
	}
	messages, err := CollectMessages(entries, nil)
	if err != nil {
		return nil, err
	}
	plan := resultPlan{
		titleSeq: -1,
		messages: cloneMessages(messages),
	}
	if accepted != nil {
		plan.titleSeq = accepted.EventSeq
	}
	if len(messages) == 0 || (accepted != nil && accepted.Source.SourceKind() != "user") {
		return plan, nil
	}
	normalized, err := FallbackSessionTitle(messages[0].Text, rules.settings.FallbackMaxWords, rules.settings.FallbackMaxBytes)
	if err != nil {
		return nil, err
	}
	if normalized == "" {
		return plan, nil
	}
	draft, err := session.NewEventDraft(TitleSet, EventData{
		Title:       normalized,
		MessageSeqs: []int64{messages[0].Seq},
		Source:      FallbackSource{Kind: "fallback"},
	})
	if err != nil {
		return nil, err
	}
	plan.draft = &draft
	return plan, nil
}

// Accept validates provider provenance and guards the result against changes
// since the input log snapshot. Explicit refresh may replace that snapshot's
// user title; a later rename is never replaced.
func (rules *Policy) Accept(entries []session.Event, identifier ProviderID, result ProviderResult, refresh bool) (session.WritePlan, error) {
	if identifier == "" {
		return nil, errors.New("sessiontitle: provider id is required")
	}
	accepted, err := Fold(entries)
	if err != nil {
		return nil, err
	}
	if accepted != nil && accepted.Source.SourceKind() == "user" && !refresh {
		return nil, ErrSuperseded
	}
	messages, err := CollectMessages(entries, nil)
	if err != nil {
		return nil, err
	}
	normalized, err := NormalizeSessionTitle(result.Title, rules.settings.MaxTitleBytes)
	if err != nil {
		return nil, err
	}
	if normalized == "" || len(result.MessageSeqs) == 0 {
		return nil, errors.New("sessiontitle: provider title and source messages are required")
	}
	order := make(map[int64]int, len(messages))
	for index, message := range messages {
		order[message.Seq] = index
	}
	previous := -1
	for _, sequence := range result.MessageSeqs {
		position, found := order[sequence]
		if !found || position <= previous {
			return nil, errors.New("sessiontitle: provider messageSeqs must be unique ordered seqs from the request")
		}
		previous = position
	}
	payload := EventData{
		Title:       normalized,
		MessageSeqs: append([]int64{}, result.MessageSeqs...),
		Source: ProviderSource{
			Kind:     "provider",
			Provider: identifier,
			Model:    cloneRoute(result.Model),
		},
	}
	if err := validateEventData(payload); err != nil {
		return nil, err
	}
	draft, err := session.NewEventDraft(TitleSet, payload)
	if err != nil {
		return nil, err
	}
	titleSeq := int64(-1)
	if accepted != nil {
		titleSeq = accepted.EventSeq
	}
	return resultPlan{
		titleSeq: titleSeq,
		messages: cloneMessages(messages),
		draft:    &draft,
	}, nil
}

type resultPlan struct {
	titleSeq int64
	messages []UserMessage
	draft    *session.EventDraft
}

func (plan resultPlan) Build(_ context.Context, snapshot session.Snapshot) ([]session.EventDraft, error) {
	accepted, err := Fold(snapshot.Events)
	if err != nil {
		return nil, err
	}
	titleSeq := int64(-1)
	if accepted != nil {
		titleSeq = accepted.EventSeq
	}
	if titleSeq != plan.titleSeq {
		return nil, ErrSuperseded
	}
	messages, err := CollectMessages(snapshot.Events, nil)
	if err != nil {
		return nil, err
	}
	if len(messages) != len(plan.messages) {
		return nil, ErrSuperseded
	}
	for index, message := range messages {
		if message != plan.messages[index] {
			return nil, ErrSuperseded
		}
	}
	if plan.draft == nil {
		return nil, nil
	}
	return []session.EventDraft{*plan.draft}, nil
}

func cloneMessages(source []UserMessage) []UserMessage {
	return append([]UserMessage(nil), source...)
}

func cloneRoute(source *ModelProvenance) *ModelProvenance {
	if source == nil {
		return nil
	}
	result := *source
	return &result
}
