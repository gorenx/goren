// Package visiblecontext owns the retained model-visible Prompt Context view.
package visiblecontext

import (
	"errors"
	"slices"
	"sync"

	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/session"
)

const (
	// messageSource is the canonical plugin identity for Prompt Context messages.
	messageSource = "@deepseek-ai/dsh-system-prompt"
	// clearedMessage invalidates an earlier non-empty Prompt Context snapshot.
	clearedMessage = "Current runtime context: none. Earlier runtime-context snapshots no longer apply."
)

// ContextSnapshot is one retained model-visible Prompt Context message.
type ContextSnapshot struct {
	Sequence int64
	Text     string
}

// VisibleContext is the exact Agent view of the retained Prompt Context.
type VisibleContext struct {
	mutex    sync.Mutex
	observed bool
	current  *ContextSnapshot
}

// New restores one VisibleContext from the current Session log and surface.
func New(conversation session.Context) (*VisibleContext, error) {
	if conversation == nil {
		return nil, errors.New("agentloop visible context: Session is required")
	}
	visible := &VisibleContext{}
	visible.restore(conversation)
	return visible, nil
}

// Observe accepts one exact Session event after it commits.
func (visible *VisibleContext) Observe(committed session.Event) {
	if visible == nil {
		return
	}
	visible.mutex.Lock()
	defer visible.mutex.Unlock()
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

// Refresh rebuilds retained Prompt Context from the current Session surface.
// RLA calls it at the step boundary, avoiding any Agent-to-Plugin listener
// registration or global per-Agent observer map.
func (visible *VisibleContext) Refresh(conversation session.Context) error {
	if visible == nil || conversation == nil {
		return errors.New("agentloop visible context: Session is required")
	}
	visible.mutex.Lock()
	visible.observed = false
	visible.current = nil
	visible.restoreLocked(conversation)
	visible.mutex.Unlock()
	return nil
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
	visible.mutex.Lock()
	observed := visible.observed
	current := visible.current
	visible.mutex.Unlock()
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
	created, err := agentmessage.NewUserMessage(agentmessage.UserMessageInput{
		Content: []agentmessage.ContentBlock{
			agentmessage.NewTextBlock(snapshotText),
		},
		Source: source,
	})
	return created, err == nil, err
}

func (visible *VisibleContext) restore(conversation session.Context) {
	visible.mutex.Lock()
	defer visible.mutex.Unlock()
	visible.restoreLocked(conversation)
}

func (visible *VisibleContext) restoreLocked(conversation session.Context) {
	retainedSequences := make(map[int64]struct{})
	// retainedSequences is a set of model-visible Session event sequences.
	// The key is a Session event sequence. The empty value is a membership
	// marker: presence means the surface currently retains that event.
	for _, sequence := range conversation.Surface().Nodes {
		retainedSequences[sequence] = struct{}{}
	}
	entries := conversation.Events()
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
