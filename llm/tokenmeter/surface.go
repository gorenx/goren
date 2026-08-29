package tokenmeter

import (
	"fmt"
	"slices"

	"github.com/gorenx/goren/session"
)

type surfaceFold struct {
	tokens int64
	nodes  []SurfaceNode
	delta  int64
}

func foldSurface(nodes []SurfaceNode, entry session.Event) (surfaceFold, error) {
	messageValue, err := session.DeriveEventMessage(entry)
	if err != nil {
		return surfaceFold{}, err
	}
	eventTokens := int64(0)
	if messageValue != nil {
		eventTokens, err = estimateMessage(messageValue)
		if err != nil {
			return surfaceFold{}, err
		}
	}
	if entry.SurfaceOp == nil {
		return surfaceFold{}, fmt.Errorf(
			"tokenmeter: surface event at seq %d has no operation",
			entry.Seq,
		)
	}
	switch entry.SurfaceOp.Kind {
	case session.SurfaceOperationAppend:
		nextNodes := append([]SurfaceNode(nil), nodes...)
		nextNodes = append(nextNodes, SurfaceNode{
			Seq:    entry.Seq,
			Tokens: eventTokens,
		})
		return surfaceFold{
			tokens: eventTokens,
			nodes:  nextNodes,
			delta:  eventTokens,
		}, nil
	case session.SurfaceOperationReplace:
		startIndex := slices.IndexFunc(nodes, func(nodeValue SurfaceNode) bool {
			return nodeValue.Seq == entry.SurfaceOp.Start
		})
		endIndex := slices.IndexFunc(nodes, func(nodeValue SurfaceNode) bool {
			return nodeValue.Seq == entry.SurfaceOp.End
		})
		if startIndex < 0 || endIndex < 0 || startIndex > endIndex {
			return surfaceFold{}, fmt.Errorf(
				"token surface: replace at seq %d has invalid current range %d-%d",
				entry.Seq,
				entry.SurfaceOp.Start,
				entry.SurfaceOp.End,
			)
		}
		removedTokens := int64(0)
		for _, nodeValue := range nodes[startIndex : endIndex+1] {
			removedTokens, err = addTokens(removedTokens, nodeValue.Tokens)
			if err != nil {
				return surfaceFold{}, err
			}
		}
		nextNodes := append([]SurfaceNode(nil), nodes...)
		nextNodes = slices.Replace(
			nextNodes,
			startIndex,
			endIndex+1,
			SurfaceNode{
				Seq:    entry.Seq,
				Tokens: eventTokens,
			},
		)
		return surfaceFold{
			tokens: eventTokens,
			nodes:  nextNodes,
			delta:  eventTokens - removedTokens,
		}, nil
	default:
		return surfaceFold{}, fmt.Errorf(
			"tokenmeter: unsupported surface operation %q",
			entry.SurfaceOp.Kind,
		)
	}
}
