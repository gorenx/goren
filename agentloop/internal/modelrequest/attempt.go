package modelrequest

import (
	"errors"

	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/systemprompt"
)

// AttemptInput contains submitted model parameters and this Step's prompt data.
// It deliberately excludes conversation messages: Start requests the committed
// Session and derives those inside the AgentLoop business boundary.
type AttemptInput struct {
	Position  session.StepPosition
	SessionID session.SessionID
	Prompt    systemprompt.PromptAssembly
	Config    llm.CallConfig
	First     bool
}

// Boundary contains the optional request facts to commit before model I/O.
type Boundary struct {
	Header  *session.EventDraft
	Context *session.EventDraft
}

// Attempt owns response assembly and the durable facts of one model attempt.
// It has no scheduling, cancellation, provider or storage responsibilities.
type Attempt struct {
	phase           attemptPhase
	baseline        []session.Event
	pendingDraft    *session.EventDraft
	messageDraft    *session.EventDraft
	confirmedNext   int64
	first           bool
	headerCommitted bool
	closed          bool
	failure         error
	pendingChunk    llm.StreamChunk
	contextDraft    *session.EventDraft
	position        session.StepPosition
	options         llm.GenerateOptions
	defaults        llm.CallConfigAdapterDefaults
	context         *llm.ModelContext
	assembly        *llm.BlockAssembler
	sequences       []int64
	message         agentmessage.AssistantMessage
	accepted        bool
}

// NewAttempt captures the proposal without opening a provider stream.
func NewAttempt(input AttemptInput) (*Attempt, error) {
	if input.Position.Turn <= 0 || input.Position.Step <= 0 || input.Position.Turn > 1<<53-1 || input.Position.Step > 1<<53-1 {
		return nil, errors.New("agentloop: model position must contain positive safe integers")
	}
	if input.SessionID == "" {
		return nil, errors.New("agentloop: model Session identity is required")
	}
	rendered, err := systemprompt.RenderPrompt(input.Prompt)
	if err != nil {
		return nil, err
	}
	initial := llm.GenerateOptions{
		CallConfig: input.Config,
		SessionID:  string(input.SessionID),
		Tools:      input.Prompt.Tools,
	}
	if rendered != "" {
		initial.System = &rendered
	}
	options, err := llm.CloneGenerateOptions(initial)
	if err != nil {
		return nil, err
	}
	result := &Attempt{
		position: input.Position,
		first:    input.First,
		options:  options,
		assembly: llm.NewBlockAssembler(),
	}

	return result, nil
}

// Prepare derives request facts from a detached log without committing them.
func (attempt *Attempt) Prepare(entries []session.Event, first bool) (Boundary, error) {
	header := session.CanonicalEpochHeader(session.EpochHeader{
		Config:          attempt.options.CallConfig,
		Tools:           attempt.options.Tools,
		System:          attempt.options.System,
		AdapterDefaults: &attempt.defaults,
	})
	previous, err := session.LatestRequestHeader(entries)
	if err != nil {
		return Boundary{}, err
	}
	draft, err := HeaderDraft(previous, header, first)
	if err != nil {
		return Boundary{}, err
	}
	route := session.RequestRouteContext{
		Provider: header.Config.Provider,
		Model:    header.Config.Model,
	}
	if attempt.context != nil {
		limit := attempt.context.ContextWindow
		route.ContextWindow = &limit
	}
	previousContext, err := session.LatestRequestContext(entries)
	if err != nil {
		return Boundary{}, err
	}
	contextDraft, err := ContextDraft(previousContext, route)
	if err != nil {
		return Boundary{}, err
	}
	attempt.options.CallConfig = header.Config
	attempt.options.System = header.System
	attempt.options.Tools = header.Tools
	return Boundary{
		Header:  draft,
		Context: contextDraft,
	}, nil
}

// ChunkDraft prepares one stream fact without advancing response assembly.
func (attempt *Attempt) ChunkDraft(chunk llm.StreamChunk) (session.EventDraft, error) {
	return session.NewEventDraft(session.AssistantChunked, session.AssistantChunk{
		Turn:  attempt.position.Turn,
		Step:  attempt.position.Step,
		Chunk: chunk,
	})
}

// ConfirmChunk folds a chunk only after the corresponding event has committed.
func (attempt *Attempt) ConfirmChunk(chunk llm.StreamChunk, events []session.Event) error {
	if attempt.accepted {
		return errors.New("agentloop: assistant response is already committed")
	}
	if len(events) != 1 {
		return errors.New("agentloop: unexpected committed assistant chunk")
	}
	draft, err := attempt.ChunkDraft(chunk)
	if err != nil {
		return err
	}
	if err := draft.CheckCommit(events[0]); err != nil {
		return err
	}
	if len(attempt.sequences) != 0 && events[0].Seq <= attempt.sequences[len(attempt.sequences)-1] {
		return errors.New("agentloop: assistant chunk sequence did not advance")
	}
	if err := attempt.assembly.Push(chunk); err != nil {
		return err
	}
	attempt.sequences = append(attempt.sequences, events[0].Seq)
	return nil
}

// MessageDraft prepares the assistant surface event. Failed finishes return nil
// so the upper request policy can decide whether to retry without a false reply.
func (attempt *Attempt) MessageDraft() (*session.EventDraft, error) {
	terminal := attempt.assembly.FinishValue()
	if terminal != nil && (terminal.ReasonKind() == "error" || terminal.ReasonKind() == "aborted") {
		return nil, nil
	}
	blocks, err := attempt.assembly.AssembledBlocks()
	if err != nil {
		return nil, err
	}
	message, err := agentmessage.NewAssistantMessage(agentmessage.AssistantMessageInput{
		Content: blocks,
		Source: agentmessage.ModelMessageSource{
			Provider:    attempt.options.Provider,
			Model:       attempt.options.Model,
			ReplayState: attempt.assembly.ReplayValue(),
		},
	})
	if err != nil {
		return nil, err
	}
	payload := session.AssistantMessage{
		Turn:    attempt.position.Turn,
		Step:    attempt.position.Step,
		Message: message,
	}
	if usage, present := attempt.assembly.UsageValue(); present {
		payload.Usage = &usage
	}
	draft, err := session.NewSurfaceEventDraft(session.AssistantMessaged, payload, session.SurfaceIntent{
		Operation:       session.SurfaceAppend(),
		SourceEventSeqs: &attempt.sequences,
	})
	if err != nil {
		return nil, err
	}
	attempt.messageDraft = &draft
	attempt.message = message
	return &draft, nil
}

// ConfirmMessage makes the assistant result visible only after its event commits.
func (attempt *Attempt) ConfirmMessage(events []session.Event) error {
	if attempt.message.StableID() == "" {
		return errors.New("agentloop: assistant message has not been prepared")
	}
	if len(events) != 1 || attempt.messageDraft == nil || attempt.accepted {
		return errors.New("agentloop: unexpected committed assistant message")
	}
	if err := attempt.messageDraft.CheckCommit(events[0]); err != nil {
		return err
	}
	attempt.accepted = true
	return nil
}

// Message returns only an acknowledged assistant message.
func (attempt *Attempt) Message() agentmessage.AssistantMessage {
	if !attempt.accepted {
		return agentmessage.AssistantMessage{}
	}
	return attempt.message
}

// Finish returns the provider's terminal reason for the upper request policy.
func (attempt *Attempt) Finish() llm.FinishReason { return attempt.assembly.FinishValue() }
