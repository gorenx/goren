package session

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
)

// Surface is a detached view of the ordered model-visible event sequences.
type Surface struct {
	Nodes             []int64
	ReplaceGeneration uint64
}

type surfaceState struct {
	nodes             []int64
	replaceGeneration uint64
}

type surfacePlan struct {
	appendNode int64
	replace    bool
	startIndex int
	endIndex   int
}

func planSurface(state surfaceState, candidate Event, entries []Event) (surfacePlan, error) {
	_, eligible := surfaceEventTypes[candidate.Type]
	if !eligible {
		if candidate.SurfaceOp != nil || candidate.SourceEventSeqs != nil {
			return surfacePlan{}, fmt.Errorf("session: event %q is not surface-eligible", candidate.Type)
		}
		return surfacePlan{appendNode: -1}, nil
	}
	if candidate.SurfaceOp == nil {
		return surfacePlan{}, fmt.Errorf("session: event %q requires surfaceOp", candidate.Type)
	}
	operation := *candidate.SurfaceOp
	switch operation.Kind {
	case SurfaceOperationAppend:
		if err := validateProvenance(candidate, nil); err != nil {
			return surfacePlan{}, err
		}
		return surfacePlan{appendNode: candidate.Seq}, nil
	case SurfaceOperationReplace:
		startIndex := slices.Index(state.nodes, operation.Start)
		if startIndex < 0 {
			return surfacePlan{}, fmt.Errorf("session: surface replace start seq %d not found", operation.Start)
		}
		endIndex := slices.Index(state.nodes, operation.End)
		if endIndex < 0 {
			return surfacePlan{}, fmt.Errorf("session: surface replace end seq %d not found", operation.End)
		}
		if startIndex > endIndex {
			return surfacePlan{}, errors.New("session: surface replace start is after end")
		}
		shadowed := state.nodes[startIndex : endIndex+1]
		if err := validateProvenance(candidate, shadowed); err != nil {
			return surfacePlan{}, err
		}
		if err := validateToolResultRewrite(candidate, shadowed, entries); err != nil {
			return surfacePlan{}, err
		}
		return surfacePlan{
			appendNode: candidate.Seq, replace: true, startIndex: startIndex, endIndex: endIndex,
		}, nil
	default:
		return surfacePlan{}, fmt.Errorf("session: unsupported surface operation %q", operation.Kind)
	}
}

func applySurface(state *surfaceState, transition surfacePlan) {
	if transition.appendNode < 0 {
		return
	}
	if !transition.replace {
		state.nodes = append(state.nodes, transition.appendNode)
		return
	}
	replacement := []int64{transition.appendNode}
	state.nodes = slices.Replace(state.nodes, transition.startIndex, transition.endIndex+1, replacement...)
	state.replaceGeneration++
}

func validateProvenance(candidate Event, shadowed []int64) error {
	if candidate.SourceEventSeqs == nil {
		if len(shadowed) == 0 {
			return nil
		}
		return errors.New("session: surface replace must cite every shadowed node")
	}
	sources := *candidate.SourceEventSeqs
	if len(sources) == 0 && candidate.Type != "assistant/message" {
		return errors.New("session: sourceEventSeqs must not be empty for this event")
	}
	seen := make(map[int64]struct{}, len(sources))
	for _, sequence := range sources {
		if !isSafeNonNegative(sequence) || sequence >= candidate.Seq {
			return fmt.Errorf("session: sourceEventSeqs must reference earlier safe sequences, got %d", sequence)
		}
		if _, exists := seen[sequence]; exists {
			return errors.New("session: sourceEventSeqs must not contain duplicates")
		}
		seen[sequence] = struct{}{}
	}
	for _, sequence := range shadowed {
		if _, exists := seen[sequence]; !exists {
			return fmt.Errorf("session: surface replace is missing shadowed seq %d", sequence)
		}
	}
	return nil
}

func validateToolResultRewrite(candidate Event, shadowed []int64, entries []Event) error {
	if candidate.Type != "tool/result" || candidate.SurfaceOp == nil || candidate.SurfaceOp.Kind != SurfaceOperationReplace {
		return nil
	}
	if len(shadowed) != 1 {
		return errors.New("session: tool/result replacement must rewrite exactly one surface node")
	}
	originalSeq := shadowed[0]
	if originalSeq < 0 || originalSeq >= int64(len(entries)) || entries[originalSeq].Type != "tool/result" {
		return errors.New("session: tool/result replacement must target tool/result")
	}
	originalShape, err := toolResultShape(entries[originalSeq].Data)
	if err != nil {
		return err
	}
	replacementShape, err := toolResultShape(candidate.Data)
	if err != nil {
		return err
	}
	if !bytes.Equal(originalShape, replacementShape) {
		return errors.New("session: tool/result replacement may change only result content")
	}
	return nil
}

func toolResultShape(rawValue json.RawMessage) ([]byte, error) {
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(rawValue, &payload); err != nil {
		return nil, errors.New("session: tool/result data must be an object")
	}
	var message map[string]json.RawMessage
	if err := json.Unmarshal(payload["message"], &message); err != nil {
		return nil, errors.New("session: tool/result message must be an object")
	}
	var content []map[string]json.RawMessage
	if err := json.Unmarshal(message["content"], &content); err != nil || len(content) != 1 {
		return nil, errors.New("session: tool/result message must contain one result block")
	}
	content[0]["content"] = json.RawMessage("null")
	normalizedContent, err := json.Marshal(content)
	if err != nil {
		return nil, err
	}
	message["content"] = normalizedContent
	normalizedMessage, err := json.Marshal(message)
	if err != nil {
		return nil, err
	}
	payload["message"] = normalizedMessage
	return json.Marshal(payload)
}
