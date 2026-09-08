package compactionactor

import (
	"github.com/gorenx/goren/compaction/basic"
	"github.com/gorenx/goren/llm"
	llmactor "github.com/gorenx/goren/llm/actor"
	goakt "github.com/tochemey/goakt/v4/actor"
)

// OnModelResolved supplies metadata to the pending invocation's admission.
func (owner *Grain) OnModelResolved(gc *goakt.GrainContext, result llmactor.ModelResolved) {
	current := owner.active
	if current == nil {
		return
	}
	if result.Err != nil {
		owner.reject(gc, current, result.Err)
		return
	}
	var capacity *int
	if result.Info.Context != nil {
		value := result.Info.Context.ContextWindow
		capacity = &value
	}
	owner.begin(gc, current, capacity)
}

func (owner *Grain) generate(gc *goakt.GrainContext, current *invocation, execution *basic.Transaction) {
	switch execution.Stage() {
	case basic.SummaryPending:
		input, err := execution.Generation()
		if err != nil {
			gc.Err(err)
			return
		}
		current.callID = owner.models.NewCall()
		owner.models.Start(
			gc,
			current.callID,
			llmactor.Parameters(input.CallConfig),
			llm.Input{
				Messages:  input.Messages,
				System:    input.System,
				Tools:     input.Tools,
				SessionID: input.SessionID,
				Purpose:   input.Purpose,
			},
			owner,
		)
	case basic.SummaryStreaming:
		owner.models.Read(gc, current.callID, owner)
	case basic.SummaryClosing:
		owner.models.Close(gc, current.callID, owner)
	}
}

// OnGenerationStarted delivers the opening outcome to the business transaction.
func (owner *Grain) OnGenerationStarted(gc *goakt.GrainContext, result llmactor.GenerationStarted) {
	current := owner.active
	if current == nil || current.callID != result.CallID {
		return
	}
	execution, err := owner.business.CurrentTransaction()
	if err != nil {
		gc.Err(err)
		return
	}
	if err := execution.Opened(result.Config, result.Err); err != nil {
		gc.Err(err)
		return
	}
	owner.advance(gc, current)
}

// OnChunkRead lets Transaction interpret stream output and end-of-stream.
func (owner *Grain) OnChunkRead(gc *goakt.GrainContext, result llmactor.ChunkRead) {
	current := owner.active
	if current == nil || current.callID != result.CallID {
		return
	}
	execution, err := owner.business.CurrentTransaction()
	if err != nil {
		gc.Err(err)
		return
	}
	if result.Err != nil || !result.Found {
		if err := execution.EndGeneration(result.Err); err != nil {
			gc.Err(err)
			return
		}
	} else {
		// Push records assembly failures and selects closure in the business state.
		_ = execution.Push(result.Chunk)
	}
	owner.advance(gc, current)
}

// OnCallClosed advances only after Transaction accepts the release outcome.
func (owner *Grain) OnCallClosed(gc *goakt.GrainContext, result llmactor.CallClosed) {
	current := owner.active
	if current == nil || current.callID != result.CallID {
		return
	}
	execution, err := owner.business.CurrentTransaction()
	if err != nil {
		gc.Err(err)
		return
	}
	if execution.ConfirmClose(result.Err) != nil {
		owner.later(gc, current, func(next *goakt.GrainContext) { owner.generate(next, current, execution) })
		return
	}
	current.callID = ""
	owner.advance(gc, current)
}
