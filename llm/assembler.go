package llm

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/gorenx/goren/internal/jsonvalue"
)

type blockAssembly struct {
	blockType string
	text      string
	callID    CallID
	callName  string
	arguments string
	completed ContentBlock
}

// BlockAssembler incrementally builds one assistant message from raw chunks.
type BlockAssembler struct {
	partials    map[int]*blockAssembly
	order       []int
	usage       *TokenUsage
	finish      FinishReason
	replayState json.RawMessage
}

// NewBlockAssembler creates empty canonical stream assembly state.
func NewBlockAssembler() *BlockAssembler {
	return &BlockAssembler{partials: make(map[int]*blockAssembly)}
}

// Push applies one chunk in stream order. The first block-end for an index is authoritative.
func (assembly *BlockAssembler) Push(entry StreamChunk) error {
	if assembly == nil {
		return errors.New("llm: block assembler is nil")
	}
	if entry == nil {
		return errors.New("llm: stream chunk is nil")
	}
	switch typedChunk := entry.(type) {
	case BlockStartChunk:
		_, err := assembly.ensure(typedChunk.Index, typedChunk.BlockType)
		return err
	case *BlockStartChunk:
		_, err := assembly.ensure(typedChunk.Index, typedChunk.BlockType)
		return err
	case TextDeltaChunk:
		return assembly.appendText(typedChunk.Index, "text", typedChunk.Text)
	case *TextDeltaChunk:
		return assembly.appendText(typedChunk.Index, "text", typedChunk.Text)
	case ReasoningDeltaChunk:
		return assembly.appendText(typedChunk.Index, "reasoning", typedChunk.Text)
	case *ReasoningDeltaChunk:
		return assembly.appendText(typedChunk.Index, "reasoning", typedChunk.Text)
	case ToolCallDeltaChunk:
		return assembly.appendTool(typedChunk)
	case *ToolCallDeltaChunk:
		return assembly.appendTool(*typedChunk)
	case BlockEndChunk:
		return assembly.endBlock(typedChunk.Index, typedChunk.Block)
	case *BlockEndChunk:
		return assembly.endBlock(typedChunk.Index, typedChunk.Block)
	case UsageChunk:
		detached := cloneUsage(typedChunk.Usage)
		assembly.usage = &detached
		return nil
	case *UsageChunk:
		detached := cloneUsage(typedChunk.Usage)
		assembly.usage = &detached
		return nil
	case FinishChunk:
		return assembly.acceptFinish(typedChunk)
	case *FinishChunk:
		return assembly.acceptFinish(*typedChunk)
	default:
		return fmt.Errorf("llm: unsupported stream chunk %q", entry.ChunkType())
	}
}

func (assembly *BlockAssembler) ensure(index int, blockType string) (*blockAssembly, error) {
	if blockType == "" {
		return nil, errors.New("llm: block type is empty")
	}
	state := assembly.partials[index]
	if state != nil {
		return state, nil
	}
	state = &blockAssembly{blockType: blockType}
	assembly.partials[index] = state
	assembly.order = append(assembly.order, index)
	return state, nil
}

func (assembly *BlockAssembler) appendText(index int, blockType string, delta string) error {
	state, err := assembly.ensure(index, blockType)
	if err != nil || state.completed != nil {
		return err
	}
	state.text += delta
	return nil
}

func (assembly *BlockAssembler) appendTool(typedChunk ToolCallDeltaChunk) error {
	state, err := assembly.ensure(typedChunk.Index, "tool-call")
	if err != nil || state.completed != nil {
		return err
	}
	state.callID = typedChunk.ID
	if typedChunk.Name != nil && *typedChunk.Name != "" {
		state.callName = *typedChunk.Name
	}
	state.arguments += typedChunk.ArgumentsDelta
	return nil
}

func (assembly *BlockAssembler) endBlock(index int, blockValue ContentBlock) error {
	if blockValue == nil {
		return errors.New("llm: block-end content is nil")
	}
	state, err := assembly.ensure(index, blockValue.ContentType())
	if err != nil || state.completed != nil {
		return err
	}
	detached, err := blockValue.CloneContent()
	if err != nil {
		return err
	}
	state.completed = detached
	return nil
}

func (assembly *BlockAssembler) acceptFinish(terminalChunk FinishChunk) error {
	if terminalChunk.Reason == nil {
		return errors.New("llm: finish reason is nil")
	}
	detachedReason, err := terminalChunk.Reason.CloneReason()
	if err != nil {
		return err
	}
	assembly.finish = detachedReason
	assembly.replayState = nil
	if len(terminalChunk.ReplayState) != 0 {
		assembly.replayState, err = jsonvalue.Clone(terminalChunk.ReplayState)
	}
	return err
}

func (assembly *BlockAssembler) assemble(state *blockAssembly, index int) (ContentBlock, error) {
	if state.completed != nil {
		return state.completed.CloneContent()
	}
	switch state.blockType {
	case "text":
		return NewTextBlock(state.text), nil
	case "reasoning":
		return ReasoningBlock{Type: "reasoning", Text: state.text}, nil
	case "tool-call":
		identifier := state.callID
		if identifier == "" {
			identifier = CallID(fmt.Sprintf("call-%d", index))
		}
		return ToolCallBlock{Type: "tool-call", ID: identifier, Name: state.callName, Arguments: state.arguments}, nil
	default:
		return nil, fmt.Errorf("llm: cannot assemble incomplete block of type %q", state.blockType)
	}
}

// AssembledBlocks returns one detached block per first-seen index.
func (assembly *BlockAssembler) AssembledBlocks() ([]ContentBlock, error) {
	if assembly == nil {
		return nil, errors.New("llm: block assembler is nil")
	}
	result := make([]ContentBlock, 0, len(assembly.order))
	for _, index := range assembly.order {
		state := assembly.partials[index]
		if state == nil {
			return nil, fmt.Errorf("llm: block assembler lost index %d", index)
		}
		detached, err := assembly.assemble(state, index)
		if err != nil {
			return nil, err
		}
		if assembly.FinishValue().ReasonKind() == "max-tokens" && detached.ContentType() == "tool-call" {
			continue
		}
		result = append(result, detached)
	}
	return result, nil
}

// UsageValue returns a detached usage snapshot when one was observed.
func (assembly *BlockAssembler) UsageValue() (TokenUsage, bool) {
	if assembly == nil || assembly.usage == nil {
		return TokenUsage{}, false
	}
	return cloneUsage(*assembly.usage), true
}

// FinishValue returns the observed finish reason or the canonical stop default.
func (assembly *BlockAssembler) FinishValue() FinishReason {
	if assembly == nil || assembly.finish == nil {
		return StopFinish{Kind: "stop"}
	}
	detached, err := assembly.finish.CloneReason()
	if err != nil {
		return ErrorFinish{Kind: "error", Failure: LlmFailure{Message: err.Error(), Code: "INVARIANT"}}
	}
	return detached
}

// ReplayValue returns detached adapter-private replay state.
func (assembly *BlockAssembler) ReplayValue() json.RawMessage {
	if assembly == nil || len(assembly.replayState) == 0 {
		return nil
	}
	return append(json.RawMessage(nil), assembly.replayState...)
}

// AssembleMessage returns an identified assistant-role message over current blocks.
func (assembly *BlockAssembler) AssembleMessage(origin MessageSource) (Message, error) {
	if origin == nil {
		origin = PluginMessageSource{Plugin: "dsh-llm/assembler"}
	}
	result, err := assembly.AssembledBlocks()
	if err != nil {
		return nil, err
	}
	return NewMessage(MessageInput{Role: RoleAssistant, Content: result, Source: origin})
}
