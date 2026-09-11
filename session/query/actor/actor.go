package queryactor

import (
	"context"
	"errors"
	"github.com/gorenx/goren/session/query"
	goakt "github.com/tochemey/goakt/v4/actor"
)

// Actor serializes complete source acquisition and Query business execution.
// Composition must enable stashing and retain the index until PostStop returns.
type Actor struct {
	service *query.Service
	sources query.Sources
	active  *queryTask
}

type queryTask struct {
	cancel context.CancelFunc
	done   chan struct{}
	result any
}

type queryCompleted struct{ task *queryTask }

// New binds the query owner to its complete source reader without opening storage.
func New(backend query.Index, settings query.Config, observations query.Sources) (*Actor, error) {
	if observations == nil {
		return nil, errors.New("session query: source reader is required")
	}
	evaluator, err := query.New(backend, settings)
	if err != nil {
		return nil, err
	}
	return &Actor{
		service: evaluator,
		sources: observations,
	}, nil
}

// PreStart leaves index and source lifecycle with composition.
func (*Actor) PreStart(*goakt.Context) error { return nil }

// PostStop waits for source and index I/O before composition may close storage.
func (owner *Actor) PostStop(*goakt.Context) error {
	if owner.active != nil {
		owner.active.cancel()
		<-owner.active.done
		owner.active = nil
	}
	return nil
}

// Receive preserves the original request in the framework stash while work runs.
func (owner *Actor) Receive(rc *goakt.ReceiveContext) {
	if _, started := rc.Message().(*goakt.PostStart); started {
		return
	}
	command := rc.Message()
	ctx, cancel := context.WithCancel(rc.Context())
	current := &queryTask{
		cancel: cancel,
		done:   make(chan struct{}),
	}
	owner.active = current
	rc.Stash()
	rc.Become(owner.awaitQuery)
	err := rc.Self().PipeTo(context.WithoutCancel(rc.Context()), rc.Self(), func() (any, error) {
		defer close(current.done)
		sources, failure := owner.sources.ReadSources(ctx)
		if failure == nil {
			failure = owner.service.Observe(sources)
		}
		current.result = owner.execute(ctx, command, failure)
		return queryCompleted{task: current}, nil
	})
	if err != nil {
		cancel()
		current.result = owner.execute(ctx, command, err)
		close(current.done)
		rc.Become(owner.replyQuery)
		rc.Unstash()
	}
}

func (owner *Actor) awaitQuery(rc *goakt.ReceiveContext) {
	completed, ok := rc.Message().(queryCompleted)
	if !ok {
		rc.Stash()
		return
	}
	if completed.task != owner.active {
		return
	}
	<-owner.active.done
	rc.Become(owner.replyQuery)
	rc.Unstash()
}

func (owner *Actor) replyQuery(rc *goakt.ReceiveContext) {
	current := owner.active
	current.cancel()
	owner.active = nil
	rc.Response(current.result)
	rc.Become(owner.Receive)
	rc.UnstashAll()
}

// execute maps named requests to the framework-independent business owner.
func (owner *Actor) execute(ctx context.Context, command any, failure error) any {
	switch input := command.(type) {
	case ListSessions:
		if failure != nil {
			return Sessions{Err: failure}
		}
		items, err := owner.service.ListSessions(ctx)
		return Sessions{
			Items: items,
			Err:   err,
		}
	case FilterSessions:
		if failure != nil {
			return Sessions{Err: failure}
		}
		items, err := owner.service.FilterSessions(ctx, input.Constraints)
		return Sessions{
			Items: items,
			Err:   err,
		}
	case ReadSession:
		if failure != nil {
			return Log{Err: failure}
		}
		value, err := owner.service.ReadSession(ctx, input.SessionID)
		return Log{
			Value: value,
			Err:   err,
		}
	case ReadTitle:
		if failure != nil {
			return Title{Err: failure}
		}
		value, err := owner.service.ReadTitle(ctx, input.SessionID)
		return Title{
			Value: value,
			Err:   err,
		}
	case ReadTitles:
		if failure != nil {
			return Titles{Err: failure}
		}
		items, err := owner.service.ReadTitleSnapshots(ctx, input.SessionIDs)
		return Titles{
			Items: items,
			Err:   err,
		}
	case ListEvents:
		if failure != nil {
			return Events{Err: failure}
		}
		items, err := owner.service.ListEvents(ctx, input.SessionID)
		return Events{
			Items: items,
			Err:   err,
		}
	case FilterEvents:
		if failure != nil {
			return Documents{Err: failure}
		}
		items, err := owner.service.FilterEvents(ctx, input.SessionID, input.Constraints, input.Text)
		return Documents{
			Items: items,
			Err:   err,
		}
	case ReadSurface:
		if failure != nil {
			return Surface{Err: failure}
		}
		value, err := owner.service.ReadSurface(ctx, input.SessionID)
		return Surface{
			Value: value,
			Err:   err,
		}
	case ReadEvent:
		if failure != nil {
			return Event{Err: failure}
		}
		value, err := owner.service.ReadEvent(ctx, input.Request)
		return Event{
			Value: value,
			Err:   err,
		}
	case TraceSession:
		if failure != nil {
			return Lineage{Err: failure}
		}
		value, err := owner.service.TraceSession(ctx, input.SessionID)
		return Lineage{
			Value: value,
			Err:   err,
		}
	case TraceEvent:
		if failure != nil {
			return Trace{Err: failure}
		}
		value, err := owner.service.TraceEvent(ctx, input.Request)
		return Trace{
			Value: value,
			Err:   err,
		}
	case SearchSessions:
		if failure != nil {
			return SessionMatches{Err: failure}
		}
		page, err := owner.service.SearchSessions(ctx, input.Request)
		return SessionMatches{
			Page: page,
			Err:  err,
		}
	case SearchEvents:
		if failure != nil {
			return EventMatches{Err: failure}
		}
		page, err := owner.service.SearchEvents(ctx, input.Request)
		return EventMatches{
			Page: page,
			Err:  err,
		}
	default:
		return Completion{Err: errors.New("session query: unsupported request")}
	}
}
