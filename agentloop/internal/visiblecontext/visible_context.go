// Package visiblecontext owns the retained model-visible Prompt Context view.
package visiblecontext

import (
	"errors"
	"slices"

	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/session"
)

const (
	// messageSource is the persisted source identity for Prompt Context messages.
	messageSource = "@deepseek-ai/dsh-system-prompt"
	// clearedMessage invalidates an earlier non-empty Prompt Context snapshot.
	clearedMessage = "Current runtime context: none. Earlier runtime-context snapshots no longer apply."
)

// ContextSnapshot is one retained model-visible Prompt Context message.
type ContextSnapshot struct {
	Sequence int64
	Text     string
}

// VisibleContext is the retained Prompt Context view owned by one AgentLoop.
// Its owner serializes access; no mutable Session object is retained.
type VisibleContext struct {
	observed bool
	current  *ContextSnapshot
}

// New restores one view from a detached, committed Session snapshot.
func New(snapshot session.Snapshot) *VisibleContext {
	visible := &VisibleContext{}
	visible.Refresh(snapshot)
	return visible
}

// Observe accepts one exact Session event after it commits.
func (visible *VisibleContext) Observe(committed session.Event) {
	if visible == nil {
		return
	}
	if text, owned := ownedMessage(committed); owned {
		visible.observed = true
		visible.current = &ContextSnapshot{
			Sequence: committed.Seq,
			Text:     text,
		}
		return
	}
	if visible.current != nil && committed.SurfaceOp != nil &&
		committed.SurfaceOp.Kind == session.SurfaceOperationReplace &&
		committed.SourceEventSeqs != nil &&
		slices.Contains(*committed.SourceEventSeqs, visible.current.Sequence) {
		visible.current = nil
	}
}

// Refresh rebuilds the view at a step boundary. The AgentLoop owner serializes
// this operation with committed-event observations and message decisions.
func (visible *VisibleContext) Refresh(snapshot session.Snapshot) {
	visible.observed = false
	visible.current = nil
	visible.restore(snapshot)
}

// Message returns the model-visible snapshot required for the assembled Prompt
// Context. An unchanged retained snapshot produces no message.
func (visible *VisibleContext) Message(
	text string,
	sections []agentmessage.ContextSnapshotSection,
) (agentmessage.UserMessage, bool, error) {
	if visible == nil {
		return agentmessage.UserMessage{}, false, errors.New(
			"agentloop visible context: VisibleContext is nil",
		)
	}
	observed := visible.observed
	current := visible.current
	if !observed && text == "" {
		return agentmessage.UserMessage{}, false, nil
	}
	snapshotText := text
	if snapshotText == "" {
		snapshotText = clearedMessage
	}
	if current != nil && current.Text == snapshotText {
		return agentmessage.UserMessage{}, false, nil
	}
	source := agentmessage.PluginMessageSource{
		Plugin: messageSource,
	}
	if len(sections) != 0 {
		source.Form = agentmessage.ContextSnapshot
		source.Sections = slices.Clone(sections)
	}
	created, err := agentmessage.NewUserMessage(
		agentmessage.UserMessageInput{
			Content: []agentmessage.ContentBlock{
				agentmessage.NewTextBlock(snapshotText),
			},
			Source: source,
		},
	)
	return created, err == nil, err
}

func (visible *VisibleContext) restore(snapshot session.Snapshot) {
	retainedSequences := make(map[int64]struct{})
	// retainedSequences is a set of model-visible Session event sequences.
	// The key is a Session event sequence. The empty value is a membership
	// marker: presence means the surface currently retains that event.
	for _, sequence := range snapshot.Surface.Nodes {
		retainedSequences[sequence] = struct{}{}
	}
	entries := snapshot.Events
	for index := len(entries) - 1; index >= 0; index-- {
		committed := entries[index]
		text, owned := ownedMessage(committed)
		if !owned {
			continue
		}
		visible.observed = true
		if _, retained := retainedSequences[committed.Seq]; retained {
			visible.current = &ContextSnapshot{
				Sequence: committed.Seq,
				Text:     text,
			}
			return
		}
	}
}

func ownedMessage(committed session.Event) (string, bool) {
	if committed.Type != session.UserMessageEventName {
		return "", false
	}
	retained, err := agentmessage.DecodeUserMessage(committed.Data)
	if err != nil {
		return "", false
	}
	source, ok := retained.SourceValue().(agentmessage.PluginMessageSource)
	if !ok || source.Plugin != messageSource {
		return "", false
	}
	blocks := retained.ContentValue()
	if len(blocks) != 1 {
		return "", false
	}
	text, ok := blocks[0].(agentmessage.TextBlock)
	if !ok {
		return "", false
	}
	return text.Text, true
}
