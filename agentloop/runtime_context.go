package agentloop

import (
	"errors"
	"slices"
	"sync"

	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/session"
)

const (
	runtimeContextSource  = "@deepseek-ai/dsh-system-prompt"
	clearedRuntimeContext = "Current runtime context: none. Earlier runtime-context snapshots no longer apply."
)

// runtimeContextProjection follows the last retained prompt-context snapshot;
// Session remains the only owner of commits and surface replacement.
type runtimeContextProjection struct {
	mu          sync.Mutex
	initialized bool
	retained    bool
	sequence    int64
	text        string
}

func newRuntimeContextProjection(
	conversation session.Context,
) (*runtimeContextProjection, error) {
	if conversation == nil {
		return nil, errors.New("agentloop: runtime-context Session is required")
	}
	projection := &runtimeContextProjection{}
	projection.restore(conversation)
	return projection, nil
}

func (projection *runtimeContextProjection) restore(conversation session.Context) {
	visible := make(map[int64]struct{})
	for _, sequence := range conversation.Surface().Nodes {
		visible[sequence] = struct{}{}
	}
	entries := conversation.Events()
	for index := len(entries) - 1; index >= 0; index-- {
		committed := entries[index]
		text, owned := ownedRuntimeContext(committed)
		if !owned {
			continue
		}
		projection.initialized = true
		if _, retained := visible[committed.Seq]; retained {
			projection.retained = true
			projection.sequence = committed.Seq
			projection.text = text
			return
		}
	}
}

func (projection *runtimeContextProjection) accept(committed session.Event) {
	projection.mu.Lock()
	defer projection.mu.Unlock()
	if text, owned := ownedRuntimeContext(committed); owned {
		projection.initialized = true
		projection.retained = true
		projection.sequence = committed.Seq
		projection.text = text
		return
	}
	if projection.retained && committed.SurfaceOp != nil &&
		committed.SurfaceOp.Kind == session.SurfaceOperationReplace && committed.SourceEventSeqs != nil &&
		slices.Contains(*committed.SourceEventSeqs, projection.sequence) {
		projection.retained = false
		projection.text = ""
	}
}

func (projection *runtimeContextProjection) project(current string, sections []llm.ContextSnapshotSection) (llm.UserMessage, bool, error) {
	projection.mu.Lock()
	initialized := projection.initialized
	retained := projection.retained
	retainedText := projection.text
	projection.mu.Unlock()
	if !initialized && current == "" {
		return llm.UserMessage{}, false, nil
	}
	snapshot := current
	if snapshot == "" {
		snapshot = clearedRuntimeContext
	}
	if retained && retainedText == snapshot {
		return llm.UserMessage{}, false, nil
	}
	origin := llm.PluginMessageSource{
		Plugin: runtimeContextSource,
	}
	if len(sections) != 0 {
		origin.Form = llm.ContextSnapshot
		origin.Sections = slices.Clone(sections)
	}
	created, err := llm.NewUserMessage(llm.UserMessageInput{
		Content: []llm.ContentBlock{
			llm.NewTextBlock(snapshot),
		},
		Source: origin,
	})
	return created, err == nil, err
}

func ownedRuntimeContext(committed session.Event) (string, bool) {
	if committed.Type != session.UserMessageEventName {
		return "", false
	}
	retained, err := llm.DecodeUserMessage(committed.Data)
	if err != nil {
		return "", false
	}
	origin, ok := retained.SourceValue().(llm.PluginMessageSource)
	if !ok || origin.Plugin != runtimeContextSource {
		return "", false
	}
	blocks := retained.ContentValue()
	if len(blocks) != 1 {
		return "", true
	}
	textBlock, ok := blocks[0].(llm.TextBlock)
	if !ok {
		return "", true
	}
	return textBlock.Text, true
}
