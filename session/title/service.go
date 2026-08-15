package title

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/session"
	sessionprojection "github.com/gorenx/goren/session/projection"
)

// Options supplies contained asynchronous error reporting.
type Options struct {
	ReportError func(error)
}

type providerRegistration struct {
	implementation Provider
	closing        bool
	active         sync.WaitGroup
}

type pendingWork struct {
	registration *providerRegistration
	revision     int64
	throughSeq   int64
}

type activeWork struct {
	pendingWork
	requestContext context.Context
	cancel         context.CancelCauseFunc
	stopCaller     func() bool
}

type fallbackCall struct {
	done     chan struct{}
	accepted *Snapshot
	err      error
}

type titleWorkState struct {
	revision int64
	fallback *fallbackCall
	pending  *pendingWork
	active   *activeWork
}

// LogService is the source-aligned in-process TitleService provider.
type LogService struct {
	store       session.Store
	projections sessionprojection.Registry
	settings    Config
	reportError func(error)

	mu           sync.Mutex
	closed       bool
	lifetime     context.Context
	cancel       context.CancelCauseFunc
	registration *providerRegistration
	work         map[*session.Session]*titleWorkState
	inFlight     sync.WaitGroup
}

// NewLogService installs the title projection and automatic scheduling hooks.
func NewLogService(
	pluginScope *plugin.Scope,
	store session.Store,
	projections sessionprojection.Registry,
	settings Config,
	serviceOptions Options,
) (*LogService, error) {
	if pluginScope == nil || store == nil || projections == nil {
		return nil, errors.New("sessiontitle: Scope, Store, and Projection Registry are required")
	}
	validated, err := settings.Validate()
	if err != nil {
		return nil, err
	}
	reporter := serviceOptions.ReportError
	if reporter == nil {
		reporter = func(error) {}
	}
	lifetime, cancel := context.WithCancelCause(context.Background())
	owner := &LogService{
		store: store, projections: projections, settings: validated, reportError: reporter,
		lifetime: lifetime, cancel: cancel, work: make(map[*session.Session]*titleWorkState),
	}
	if _, err := projections.Register(pluginScope, titleProjectionUnit{}); err != nil {
		return nil, err
	}
	if _, err := session.OnEvent(pluginScope, owner.observeEvent); err != nil {
		return nil, err
	}
	if _, err := plugin.OnWaterfall(pluginScope, llm.StreamEvent, owner.observeStream); err != nil {
		return nil, err
	}
	if _, err := session.OnDisposed(pluginScope, owner.observeDisposed); err != nil {
		return nil, err
	}
	if _, err := plugin.Own(pluginScope, "sessionTitle lifecycle", owner.close); err != nil {
		return nil, err
	}
	return owner, nil
}

// Get reads the latest title solely from the Session log.
func (owner *LogService) Get(conversation *session.Session) (*Snapshot, error) {
	if conversation == nil {
		return nil, errors.New("sessiontitle: title Session is nil")
	}
	accepted, err := Fold(conversation.Events())
	return cloneSnapshot(accepted), err
}

// Rename appends one normalized user-source title and pins automation.
func (owner *LogService) Rename(conversation *session.Session, title string) (*Snapshot, error) {
	if err := owner.assertLive(conversation); err != nil {
		return nil, err
	}
	normalized, err := NormalizeSessionTitle(title, owner.settings.MaxTitleBytes)
	if err != nil {
		return nil, err
	}
	if normalized == "" {
		return nil, &SessionTitleInvalidError{Message: "session title must contain visible characters"}
	}
	owner.mu.Lock()
	state := owner.stateForLocked(conversation)
	owner.supersedeLocked(state, errors.New("user rename superseded automatic title generation"))
	owner.mu.Unlock()
	if _, err := session.AppendSerialized(conversation, TitleSet, EventData{
		Title: normalized, MessageSeqs: []int64{}, Source: UserSource{Kind: "user"},
	}); err != nil {
		return nil, err
	}
	accepted, err := owner.Get(conversation)
	if err != nil {
		return nil, err
	}
	if accepted == nil {
		return nil, errors.New("sessiontitle: renamed title failed to fold")
	}
	return accepted, nil
}

// Refresh deliberately unpins a user title and regenerates from the current log.
func (owner *LogService) Refresh(requestContext context.Context, conversation *session.Session) (*Snapshot, error) {
	if requestContext == nil {
		return nil, errors.New("sessiontitle: refresh Context is nil")
	}
	if err := requestContext.Err(); err != nil {
		return nil, err
	}
	if err := owner.assertLive(conversation); err != nil {
		return nil, err
	}
	messages, err := CollectMessages(conversation.Events(), nil)
	if err != nil {
		return nil, err
	}
	owner.mu.Lock()
	registration := owner.registration
	providerAvailable := registration != nil && !registration.closing && len(messages) != 0
	owner.mu.Unlock()
	if !providerAvailable {
		accepted, readErr := owner.Get(conversation)
		if readErr != nil {
			return nil, readErr
		}
		if accepted != nil && accepted.Source.SourceKind() == "user" && len(messages) != 0 {
			if appendErr := owner.appendFallback(conversation, messages[0]); appendErr != nil {
				return nil, appendErr
			}
			if err := requestContext.Err(); err != nil {
				return nil, err
			}
			return owner.Get(conversation)
		}
		return owner.ensureFallback(requestContext, conversation)
	}

	latest := messages[len(messages)-1]
	owner.mu.Lock()
	state := owner.stateForLocked(conversation)
	revision := owner.supersedeLocked(state, errors.New("explicit title refresh superseded older generation"))
	work := owner.activateLocked(requestContext, state, pendingWork{
		registration: registration, revision: revision, throughSeq: latest.Seq,
	})
	owner.mu.Unlock()
	route, routeErr := currentRoute(conversation)
	if routeErr != nil {
		owner.finishActive(conversation, work)
		return nil, routeErr
	}
	return owner.runTrackedProvider(conversation, work, route)
}

// Register installs the sole optional title provider for the owner Scope.
func (owner *LogService) Register(ownerScope *plugin.Scope, implementation Provider) (plugin.Disposer, error) {
	if ownerScope == nil || implementation == nil {
		return nil, errors.New("sessiontitle: provider Scope and implementation are required")
	}
	identifier := implementation.ID()
	mode := implementation.AutomaticMode()
	if identifier == "" {
		return nil, errors.New("sessiontitle: provider id must be non-empty")
	}
	if mode != AutomaticFirstPrompt && mode != AutomaticAllPrompts {
		return nil, errors.New("sessiontitle: provider automatic mode is invalid")
	}
	registration := &providerRegistration{implementation: implementation}
	owner.mu.Lock()
	if owner.closed {
		owner.mu.Unlock()
		return nil, errors.New("sessiontitle: service is disposed")
	}
	if owner.registration != nil {
		existingID := owner.registration.implementation.ID()
		owner.mu.Unlock()
		return nil, fmt.Errorf("sessiontitle: provider %q is already registered", existingID)
	}
	owner.registration = registration
	owner.mu.Unlock()
	release, err := plugin.Own(ownerScope, "sessionTitle.register()", func(closeContext context.Context) error {
		return owner.disposeRegistration(closeContext, registration)
	})
	if err != nil {
		_ = owner.disposeRegistration(context.Background(), registration)
		return nil, err
	}
	return release, nil
}

func (owner *LogService) observeEvent(
	requestContext context.Context,
	conversation *session.Session,
	committed session.Event,
) error {
	switch committed.Type {
	case session.UserMessageEventName:
		return owner.onUserMessage(requestContext, conversation, committed)
	case session.RequestHeaderEventName:
		return owner.onRequestHeader(requestContext, conversation, committed)
	default:
		return nil
	}
}

func (owner *LogService) onUserMessage(
	requestContext context.Context,
	conversation *session.Session,
	committed session.Event,
) error {
	boundary := committed.Seq
	eligible, err := CollectMessages([]session.Event{committed}, &boundary)
	if err != nil || len(eligible) == 0 {
		return err
	}
	accepted, err := owner.Get(conversation)
	if err != nil {
		return err
	}
	if accepted != nil && accepted.Source.SourceKind() == "user" {
		return nil
	}

	owner.mu.Lock()
	if owner.closed {
		owner.mu.Unlock()
		return nil
	}
	registration := owner.registration
	providerAvailable := registration != nil && !registration.closing
	owner.mu.Unlock()
	if providerAvailable {
		messages, collectErr := CollectMessages(conversation.Events(), &boundary)
		if collectErr != nil {
			return collectErr
		}
		header := conversation.Header()
		shouldSchedule := registration.implementation.AutomaticMode() == AutomaticAllPrompts ||
			(header.ParentSession == nil && len(messages) == 1 && accepted == nil)
		if shouldSchedule {
			owner.mu.Lock()
			if !owner.closed && owner.registration == registration && !registration.closing {
				state := owner.stateForLocked(conversation)
				revision := owner.supersedeLocked(state, errors.New("newer user message superseded title generation"))
				state.pending = &pendingWork{
					registration: registration, revision: revision, throughSeq: committed.Seq,
				}
			}
			owner.mu.Unlock()
		}
	}
	return session.DeferAfterEvent(requestContext, func() {
		owner.deferTask(func() {
			if _, fallbackErr := owner.ensureFallback(owner.lifetime, conversation); fallbackErr != nil &&
				!errors.Is(fallbackErr, context.Canceled) {
				owner.reportError(fmt.Errorf("sessiontitle: session %q fallback: %w", conversation.ID(), fallbackErr))
			}
		})
	})
}

func (owner *LogService) onRequestHeader(
	requestContext context.Context,
	conversation *session.Session,
	committed session.Event,
) error {
	var wireValue struct {
		Header struct {
			Config struct {
				Provider string `json:"provider"`
				Model    string `json:"model"`
			} `json:"config"`
		} `json:"header"`
	}
	if err := json.Unmarshal(committed.Data, &wireValue); err != nil {
		return err
	}
	owner.mu.Lock()
	state := owner.work[conversation]
	if owner.closed || state == nil || state.pending == nil || state.pending.throughSeq >= committed.Seq {
		owner.mu.Unlock()
		return nil
	}
	pending := *state.pending
	state.pending = nil
	owner.mu.Unlock()
	route := ModelProvenance{Provider: wireValue.Header.Config.Provider, Model: wireValue.Header.Config.Model}
	return session.DeferAfterEvent(requestContext, func() {
		owner.startPending(conversation, state, pending, route)
	})
}

func (owner *LogService) observeStream(
	requestContext context.Context,
	generationOptions llm.GenerateOptions,
	next plugin.Next[llm.GenerateOptions, llm.ChunkStream],
) (llm.ChunkStream, error) {
	owner.onMainRequest(generationOptions)
	return next(requestContext, generationOptions)
}

func (owner *LogService) onMainRequest(generationOptions llm.GenerateOptions) {
	if generationOptions.SessionID == "" || generationOptions.Purpose != "" {
		return
	}
	conversation, found := owner.store.Get(session.SessionID(generationOptions.SessionID))
	if !found {
		return
	}
	owner.mu.Lock()
	state := owner.work[conversation]
	if owner.closed || state == nil || state.pending == nil {
		owner.mu.Unlock()
		return
	}
	pending := *state.pending
	owner.mu.Unlock()
	events := conversation.Events()
	var boundary *session.Event
	for index := len(events) - 1; index >= 0; index-- {
		if events[index].Type == session.StepStartEventName || events[index].Type == session.StepEndEventName {
			copyValue := events[index]
			boundary = &copyValue
			break
		}
	}
	route, err := currentRoute(conversation)
	if err != nil || route == nil || boundary == nil || boundary.Type != session.StepStartEventName ||
		boundary.Seq <= pending.throughSeq || route.Provider != generationOptions.Provider || route.Model != generationOptions.Model {
		return
	}
	owner.mu.Lock()
	if state.pending == nil || state.pending.revision != pending.revision {
		owner.mu.Unlock()
		return
	}
	state.pending = nil
	owner.mu.Unlock()
	owner.startPending(conversation, state, pending, *route)
}

func (owner *LogService) startPending(
	conversation *session.Session,
	state *titleWorkState,
	pending pendingWork,
	route ModelProvenance,
) {
	owner.mu.Lock()
	if owner.closed || owner.registration != pending.registration || pending.registration.closing ||
		owner.work[conversation] != state || state.revision != pending.revision {
		owner.mu.Unlock()
		return
	}
	work := owner.activateLocked(owner.lifetime, state, pending)
	owner.mu.Unlock()
	if !owner.deferTask(
		func() {
			if _, err := owner.runTrackedProvider(conversation, work, &route); err != nil &&
				!errors.Is(err, context.Canceled) {
				owner.reportError(fmt.Errorf("sessiontitle: session %q automatic provider: %w", conversation.ID(), err))
			}
		},
	) {
		owner.finishActive(conversation, work)
	}
}

func (owner *LogService) runTrackedProvider(
	conversation *session.Session,
	work *activeWork,
	route *ModelProvenance,
) (*Snapshot, error) {
	defer owner.finishActive(conversation, work)
	if err := owner.assertCurrent(conversation, work); err != nil {
		return nil, err
	}
	if _, err := owner.ensureFallback(work.requestContext, conversation); err != nil {
		return nil, err
	}
	if err := owner.assertCurrent(conversation, work); err != nil {
		return nil, err
	}
	throughSeq := work.throughSeq
	messages, err := CollectMessages(conversation.Events(), &throughSeq)
	if err != nil {
		return nil, err
	}
	request := ProviderRequest{
		Session: conversation, Messages: cloneMessages(messages), Route: cloneRoute(route),
	}
	result, err := work.registration.implementation.Generate(work.requestContext, request)
	if err != nil {
		return nil, err
	}
	if err := owner.assertCurrent(conversation, work); err != nil {
		return nil, err
	}
	accepted, err := owner.validateResult(result, messages)
	if err != nil {
		return nil, err
	}
	source := ProviderSource{Kind: "provider", Provider: work.registration.implementation.ID(), Model: cloneRoute(accepted.Model)}
	if _, err := session.AppendSerialized(conversation, TitleSet, EventData{
		Title: accepted.Title, MessageSeqs: append([]int64{}, accepted.MessageSeqs...), Source: source,
	}); err != nil {
		return nil, err
	}
	return owner.Get(conversation)
}

func (owner *LogService) validateResult(result ProviderResult, messages []UserMessage) (ProviderResult, error) {
	title, err := NormalizeSessionTitle(result.Title, owner.settings.MaxTitleBytes)
	if err != nil {
		return ProviderResult{}, err
	}
	if title == "" {
		return ProviderResult{}, errors.New("sessiontitle: provider returned an empty title")
	}
	if len(result.MessageSeqs) == 0 {
		return ProviderResult{}, errors.New("sessiontitle: provider must identify at least one source message seq")
	}
	order := make(map[int64]int, len(messages))
	for index, message := range messages {
		order[message.Seq] = index
	}
	previous := -1
	sequences := make([]int64, len(result.MessageSeqs))
	for index, sequence := range result.MessageSeqs {
		position, found := order[sequence]
		if !found || sequence < 0 || position <= previous {
			return ProviderResult{}, errors.New("sessiontitle: provider messageSeqs must be unique ordered seqs from the request")
		}
		sequences[index] = sequence
		previous = position
	}
	modelRoute := cloneRoute(result.Model)
	if modelRoute != nil && (modelRoute.Provider == "" || modelRoute.Model == "") {
		return ProviderResult{}, errors.New("sessiontitle: provider result model route is incomplete")
	}
	return ProviderResult{Title: title, MessageSeqs: sequences, Model: modelRoute}, nil
}

func (owner *LogService) ensureFallback(
	requestContext context.Context,
	conversation *session.Session,
) (*Snapshot, error) {
	if err := requestContext.Err(); err != nil {
		return nil, err
	}
	if err := owner.assertLive(conversation); err != nil {
		return nil, err
	}
	accepted, err := owner.Get(conversation)
	if err != nil || accepted != nil {
		return accepted, err
	}
	messages, err := CollectMessages(conversation.Events(), nil)
	if err != nil || len(messages) == 0 {
		return nil, err
	}
	title, err := FallbackSessionTitle(
		messages[0].Text, owner.settings.FallbackMaxWords, owner.settings.FallbackMaxBytes,
	)
	if err != nil || title == "" {
		return nil, err
	}

	owner.mu.Lock()
	if owner.closed {
		owner.mu.Unlock()
		return nil, errors.New("sessiontitle: service is disposed")
	}
	state := owner.stateForLocked(conversation)
	if state.fallback != nil {
		call := state.fallback
		owner.mu.Unlock()
		select {
		case <-requestContext.Done():
			return nil, requestContext.Err()
		case <-call.done:
			return cloneSnapshot(call.accepted), call.err
		}
	}
	call := &fallbackCall{done: make(chan struct{})}
	state.fallback = call
	owner.mu.Unlock()

	if liveErr := owner.assertLive(conversation); liveErr != nil {
		err = liveErr
	} else {
		accepted, err = owner.Get(conversation)
	}
	if err == nil && accepted == nil {
		_, err = session.AppendSerialized(conversation, TitleSet, EventData{
			Title: title, MessageSeqs: []int64{messages[0].Seq}, Source: FallbackSource{Kind: "fallback"},
		})
		if err == nil {
			accepted, err = owner.Get(conversation)
		}
	}
	owner.mu.Lock()
	if state.fallback == call {
		state.fallback = nil
	}
	call.accepted = cloneSnapshot(accepted)
	call.err = err
	close(call.done)
	owner.mu.Unlock()
	return accepted, err
}

func (owner *LogService) appendFallback(conversation *session.Session, first UserMessage) error {
	if err := owner.assertLive(conversation); err != nil {
		return err
	}
	title, err := FallbackSessionTitle(
		first.Text, owner.settings.FallbackMaxWords, owner.settings.FallbackMaxBytes,
	)
	if err != nil || title == "" {
		return err
	}
	_, err = session.AppendSerialized(conversation, TitleSet, EventData{
		Title: title, MessageSeqs: []int64{first.Seq}, Source: FallbackSource{Kind: "fallback"},
	})
	return err
}

func (owner *LogService) assertLive(conversation *session.Session) error {
	if conversation == nil {
		return errors.New("sessiontitle: Session is nil")
	}
	owner.mu.Lock()
	closed := owner.closed
	owner.mu.Unlock()
	if closed {
		return errors.New("sessiontitle: service is disposed")
	}
	live, found := owner.store.Get(conversation.ID())
	if !found || live != conversation {
		return fmt.Errorf("sessiontitle: session %q is not live in this Store", conversation.ID())
	}
	return nil
}

func (owner *LogService) assertCurrent(conversation *session.Session, work *activeWork) error {
	if err := work.requestContext.Err(); err != nil {
		return err
	}
	owner.mu.Lock()
	current := owner.work[conversation]
	valid := !owner.closed && owner.registration == work.registration && !work.registration.closing &&
		current != nil && current.active == work && current.revision == work.revision
	owner.mu.Unlock()
	if !valid {
		return context.Canceled
	}
	live, found := owner.store.Get(conversation.ID())
	if !found || live != conversation {
		return context.Canceled
	}
	return nil
}

func (owner *LogService) activateLocked(
	upstream context.Context,
	state *titleWorkState,
	pending pendingWork,
) *activeWork {
	requestContext, cancel := context.WithCancelCause(owner.lifetime)
	stopCaller := context.AfterFunc(upstream, func() {
		cancel(context.Cause(upstream))
	})
	work := &activeWork{
		pendingWork: pending, requestContext: requestContext, cancel: cancel, stopCaller: stopCaller,
	}
	pending.registration.active.Add(1)
	state.active = work
	return work
}

func (owner *LogService) supersedeLocked(state *titleWorkState, cause error) int64 {
	if state.active != nil {
		state.active.cancel(cause)
	}
	state.pending = nil
	state.revision++
	return state.revision
}

func (owner *LogService) stateForLocked(conversation *session.Session) *titleWorkState {
	state := owner.work[conversation]
	if state == nil {
		state = &titleWorkState{}
		owner.work[conversation] = state
	}
	return state
}

func (owner *LogService) finishActive(conversation *session.Session, work *activeWork) {
	work.stopCaller()
	owner.mu.Lock()
	state := owner.work[conversation]
	if state != nil && state.active == work {
		state.active = nil
	}
	owner.mu.Unlock()
	work.registration.active.Done()
}

func (owner *LogService) observeDisposed(_ context.Context, conversation *session.Session) error {
	owner.mu.Lock()
	state := owner.work[conversation]
	if state != nil && state.active != nil {
		state.active.cancel(errors.New("session disposed during title generation"))
	}
	delete(owner.work, conversation)
	owner.mu.Unlock()
	return nil
}

func (owner *LogService) deferTask(task func()) bool {
	owner.mu.Lock()
	if owner.closed {
		owner.mu.Unlock()
		return false
	}
	owner.inFlight.Add(1)
	owner.mu.Unlock()
	go func() {
		defer owner.inFlight.Done()
		task()
	}()
	return true
}

func (owner *LogService) disposeRegistration(
	closeContext context.Context,
	registration *providerRegistration,
) error {
	owner.mu.Lock()
	if owner.registration != registration {
		owner.mu.Unlock()
		return nil
	}
	registration.closing = true
	for _, state := range owner.work {
		if state.pending != nil && state.pending.registration == registration {
			state.pending = nil
		}
		if state.active != nil && state.active.registration == registration {
			state.active.cancel(errors.New("session title provider was disposed"))
		}
	}
	owner.mu.Unlock()
	if err := waitGroup(closeContext, &registration.active); err != nil {
		return err
	}
	owner.mu.Lock()
	if owner.registration == registration {
		owner.registration = nil
	}
	owner.mu.Unlock()
	return nil
}

func (owner *LogService) close(closeContext context.Context) error {
	owner.mu.Lock()
	if owner.closed {
		owner.mu.Unlock()
		return nil
	}
	owner.closed = true
	owner.cancel(errors.New("session-title service disposed"))
	registration := owner.registration
	if registration != nil {
		registration.closing = true
	}
	for _, state := range owner.work {
		state.pending = nil
		if state.active != nil {
			state.active.cancel(errors.New("session-title service disposed"))
		}
	}
	owner.mu.Unlock()
	if err := waitGroup(closeContext, &owner.inFlight); err != nil {
		return err
	}
	if registration != nil {
		if err := waitGroup(closeContext, &registration.active); err != nil {
			return err
		}
	}
	owner.mu.Lock()
	owner.registration = nil
	clear(owner.work)
	owner.mu.Unlock()
	return nil
}

func waitGroup(requestContext context.Context, group *sync.WaitGroup) error {
	done := make(chan struct{})
	go func() {
		group.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-requestContext.Done():
		return requestContext.Err()
	}
}

func currentRoute(conversation *session.Session) (*ModelProvenance, error) {
	header, found, err := conversation.RequestHeaderValue()
	if err != nil || !found {
		return nil, err
	}
	return &ModelProvenance{Provider: header.Config.Provider, Model: header.Config.Model}, nil
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
