package basic

import (
	"context"
	"errors"
	"fmt"
	"reflect"

	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/compaction"
	"github.com/gorenx/goren/session"
)

// transactionCompletion fixes the three-event successful outcome of a compaction.
// Its immutable drafts and captured selection can be retried after a lost reply.
type transactionCompletion struct {
	lifecycle  compaction.Start
	startSeq   int64
	selected   region
	surface    []int64
	summary    session.EventDraft
	checkpoint agentmessage.UserMessage
	end        session.EventDraft
}

func newTransactionCompletion(start session.Event, replacement regionReplacement) (*transactionCompletion, error) {
	if start.Type != compaction.StartEventName || start.Seq < 0 {
		return nil, errors.New("compaction: completion requires a confirmed start event")
	}
	lifecycle, err := compaction.DecodeStart(start.Data)
	if err != nil {
		return nil, err
	}
	origin, ok := replacement.checkpoint.SourceValue().(compaction.CheckpointSource)
	if !ok || origin.CompactionID != lifecycle.CompactionID || !reflect.DeepEqual(origin.SourceCommandID, lifecycle.SourceCommandID) {
		return nil, errors.New("compaction: checkpoint belongs to a different transaction")
	}
	checkpoint, err := agentmessage.CloneUserMessage(replacement.checkpoint)
	if err != nil {
		return nil, err
	}
	selected := replacement.baseline.selected
	selected.sequences = append([]int64(nil), selected.sequences...)
	summary, err := session.NewEventDraft(compaction.SummaryEvent,
		compaction.Summary{
			CompactionID:    lifecycle.CompactionID,
			SourceCommandID: lifecycle.SourceCommandID,
			Summary:         replacement.generated.Content,
			RawOutput:       replacement.generated.Output,
			LLMStreamCall:   true,
			ShadowedRange: compaction.SurfaceRange{
				Start: selected.start,
				End:   selected.end,
			},
			ShadowedSeqs:       selected.sequences,
			ShadowedTokenCount: replacement.baseline.tokenCount,
			Provider:           replacement.settings.Provider,
			Model:              replacement.settings.Model,
			MaxTokens:          replacement.settings.MaxTokens,
			Usage:              replacement.generated.Usage,
		})
	if err != nil {
		return nil, err
	}
	end, err := session.NewEventDraft(compaction.EndEvent, compaction.End{
		CompactionID:    lifecycle.CompactionID,
		SourceCommandID: lifecycle.SourceCommandID,
		Turn:            lifecycle.Turn,
	})
	if err != nil {
		return nil, err
	}
	return &transactionCompletion{
		lifecycle:  lifecycle,
		startSeq:   start.Seq,
		selected:   selected,
		surface:    append([]int64(nil), replacement.baseline.surface...),
		summary:    summary,
		checkpoint: checkpoint,
		end:        end,
	}, nil
}

func (proposal *transactionCompletion) Build(_ context.Context, snapshot session.Snapshot) ([]session.EventDraft, error) {
	state, err := compaction.InspectLog(snapshot.Events)
	if err != nil {
		return nil, err
	}
	if state.Attempt == nil || state.Attempt.CompactionID != proposal.lifecycle.CompactionID || state.Attempt.StartSeq != proposal.startSeq || state.Attempt.Summarized {
		return nil, errors.New("compaction: completion does not match the active unsummarized transaction")
	}
	if proposal.startSeq >= int64(len(snapshot.Events)) {
		return nil, errors.New("compaction: start event is missing")
	}
	start, err := session.NewEventDraft(compaction.StartEvent, proposal.lifecycle)
	if err != nil {
		return nil, err
	}
	if err := start.CheckCommit(snapshot.Events[proposal.startSeq]); err != nil {
		return nil, err
	}
	if proposal.lifecycle.Turn != nil && !equalSequences(snapshot.Surface.Nodes, proposal.surface) {
		return nil, changedSurface("compaction: Session Surface changed during summarization")
	}
	current, err := locateRegion(snapshot, compaction.SurfaceRange{
		Start: proposal.selected.start,
		End:   proposal.selected.end,
	})
	if err != nil {
		return nil, &surfaceChangedError{
			message: "compaction: selected span changed during summarization",
			cause:   err,
		}
	}
	if !equalSequences(current.sequences, proposal.selected.sequences) {
		return nil, changedSurface("compaction: selected span changed during summarization")
	}
	return proposal.drafts(snapshot.Barrier.NextSeq)
}

func (proposal *transactionCompletion) drafts(summarySeq int64) ([]session.EventDraft, error) {
	sources := append([]int64{proposal.startSeq, summarySeq}, proposal.selected.sequences...)
	replacement, err := session.NewSurfaceEventDraft(
		session.UserMessageAdded,
		proposal.checkpoint,
		session.SurfaceIntent{
			Operation:       session.SurfaceReplace(proposal.selected.start, proposal.selected.end),
			SourceEventSeqs: &sources,
		},
	)
	if err != nil {
		return nil, err
	}
	return []session.EventDraft{proposal.summary, replacement, proposal.end}, nil
}

func (proposal *transactionCompletion) FindCommit(snapshot session.Snapshot) (*session.WriteResult, error) {
	for index, entry := range snapshot.Events {
		switch entry.Type {
		case compaction.EndEventName:
			ended, err := compaction.DecodeEnd(entry.Data)
			if err != nil {
				return nil, err
			}
			if ended.CompactionID == proposal.lifecycle.CompactionID {
				return nil, errors.New("compaction: transaction ended without the expected successful batch")
			}
		case compaction.SummaryEventName:
			summary, err := compaction.DecodeSummary(entry.Data)
			if err != nil {
				return nil, err
			}
			if summary.CompactionID != proposal.lifecycle.CompactionID {
				continue
			}
			if index+3 > len(snapshot.Events) {
				return nil, errors.New("compaction: incomplete successful transaction batch")
			}
			drafts, err := proposal.drafts(entry.Seq)
			if err != nil {
				return nil, err
			}
			for offset, draft := range drafts {
				stored := snapshot.Events[index+offset]
				if stored.Seq != entry.Seq+int64(offset) {
					return nil, errors.New("compaction: completion batch is not contiguous")
				}
				if err := draft.CheckCommit(stored); err != nil {
					return nil, fmt.Errorf("compaction: completion identity conflicts with committed batch: %w", err)
				}
			}
			return &session.WriteResult{
				FirstSeq: entry.Seq,
				NextSeq:  entry.Seq + 3,
				Barrier: session.WriteBarrier{
					SessionID: snapshot.Barrier.SessionID,
					NextSeq:   entry.Seq + 3,
				},
				Events: snapshot.Events[index : index+3],
			}, nil
		}
	}
	return nil, nil
}

var _ session.IdempotentWrite = (*transactionCompletion)(nil)
