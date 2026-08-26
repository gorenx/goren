//go:build contract

package execution

import (
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/session"
	contractfixture "github.com/gorenx/goren/tests/contract/fixture"
)

type outputContractBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type outputContractObservation struct {
	Name   string                `json:"name"`
	Output []outputContractBlock `json:"output"`
}

func TestPinnedSourceAssistantOutputMatchesGo(t *testing.T) {
	repositoryRoot, sourceRoot := contractfixture.Paths(t)
	sourceCommit := contractfixture.SourceCommit(
		t,
		filepath.Join(
			repositoryRoot,
			"subagent",
			"testdata",
			"source-baseline.json",
		),
	)
	requestContext, cancelRequest := context.WithTimeout(
		context.Background(),
		30*time.Second,
	)
	defer cancelRequest()
	sourceOutput, sourceErr := contractfixture.RunTypeScript(
		requestContext,
		sourceRoot,
		nil,
		filepath.Join(
			repositoryRoot,
			"tests",
			"contract",
			"typescript",
			"subagent-assistant-output.ts",
		),
		sourceRoot,
		sourceCommit,
	)
	if sourceErr != nil {
		t.Fatal(sourceErr)
	}
	var sourceObservations []outputContractObservation
	if decodeErr := json.Unmarshal(sourceOutput, &sourceObservations); decodeErr != nil {
		t.Fatal(decodeErr)
	}
	goObservations := []outputContractObservation{
		observeOutput(t, "last-non-empty-message", []session.Event{
			assistantMessageEvent(t, 1, llm.NewTextBlock("step one")),
			assistantMessageEvent(t, 2, llm.NewTextBlock("step two")),
			assistantMessageEvent(t, 3),
		}),
		observeOutput(t, "message-over-stream", []session.Event{
			assistantChunkEvent(t, 1, llm.TextDeltaChunk{
				Index: 0,
				Text:  "earlier partial",
			}),
			assistantMessageEvent(t, 2, llm.NewTextBlock("complete answer")),
			assistantChunkEvent(t, 3, llm.TextDeltaChunk{
				Index: 0,
				Text:  "later partial",
			}),
			assistantMessageEvent(t, 4),
		}),
		observeOutput(t, "reasoning-message", []session.Event{
			assistantChunkEvent(t, 1, llm.TextDeltaChunk{
				Index: 0,
				Text:  "streamed text",
			}),
			assistantMessageEvent(t, 2, llm.ReasoningBlock{
				Type: "reasoning",
				Text: "complete reasoning",
			}),
		}),
		observeOutput(t, "text-fallback", []session.Event{
			assistantChunkEvent(t, 1, llm.ReasoningDeltaChunk{
				Index: 0,
				Text:  "thinking",
			}),
			assistantChunkEvent(t, 2, llm.TextDeltaChunk{
				Index: 0,
				Text:  "partial ",
			}),
			{
				Type: session.ToolResultEventName,
				Seq:  3,
				Data: json.RawMessage(`{}`),
			},
			assistantChunkEvent(t, 4, llm.TextDeltaChunk{
				Index: 0,
				Text:  "answer",
			}),
			assistantMessageEvent(t, 5),
		}),
		observeOutput(t, "no-output", []session.Event{
			assistantChunkEvent(t, 1, llm.ReasoningDeltaChunk{
				Index: 0,
				Text:  "thinking",
			}),
			assistantMessageEvent(t, 2),
		}),
	}
	if !reflect.DeepEqual(goObservations, sourceObservations) {
		t.Fatalf(
			"Go observations = %#v, source observations = %#v",
			goObservations,
			sourceObservations,
		)
	}
}

func observeOutput(
	t *testing.T,
	name string,
	events []session.Event,
) outputContractObservation {
	t.Helper()
	selected, selectErr := SelectAssistantOutput(events)
	if selectErr != nil {
		t.Fatal(selectErr)
	}
	rawValue, encodeErr := json.Marshal(selected)
	if encodeErr != nil {
		t.Fatal(encodeErr)
	}
	var output []outputContractBlock
	if decodeErr := json.Unmarshal(rawValue, &output); decodeErr != nil {
		t.Fatal(decodeErr)
	}
	return outputContractObservation{
		Name:   name,
		Output: output,
	}
}
