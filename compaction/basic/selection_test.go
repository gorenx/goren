package basic

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/compaction"
	"github.com/gorenx/goren/llm/tokenmeter"
	"github.com/gorenx/goren/session"
)

func selectionSnapshot(t *testing.T, count int) (session.Snapshot, tokenmeter.Measurement) {
	t.Helper()
	snapshot := session.Snapshot{}
	measurement := tokenmeter.Measurement{}
	for index := range count {
		entry, err := agentmessage.NewUserMessage(agentmessage.UserMessageInput{
			Content: []agentmessage.ContentBlock{agentmessage.NewTextBlock("history")},
			Source:  agentmessage.UserMessageSource{},
		})
		if err != nil {
			t.Fatal(err)
		}
		encoded, err := json.Marshal(entry)
		if err != nil {
			t.Fatal(err)
		}
		sequence := int64(index)
		snapshot.Events = append(snapshot.Events, session.Event{
			Type: session.UserMessageEventName,
			Seq:  sequence,
			Data: encoded,
		})
		snapshot.Surface.Nodes = append(snapshot.Surface.Nodes, sequence)
		measurement.Nodes = append(measurement.Nodes, tokenmeter.SurfaceNode{
			Seq:    sequence,
			Tokens: 10,
		})
	}
	snapshot.Barrier.NextSeq = int64(count)
	measurement.LogRevision = int64(count)
	return snapshot, measurement
}

func TestSelectionRetainsTailBySurfacePosition(t *testing.T) {
	for _, scenario := range []struct {
		name     string
		count    int
		retain   int64
		end      int64
		selected bool
	}{
		{
			name: "empty",
		},
		{
			name:  "one node",
			count: 1,
		},
		{
			name:     "zero retains newest",
			count:    4,
			end:      2,
			selected: true,
		},
		{
			name:     "budget retains two",
			count:    4,
			retain:   15,
			end:      1,
			selected: true,
		},
		{
			name:   "all retained",
			count:  4,
			retain: 40,
		},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			snapshot, measurement := selectionSnapshot(t, scenario.count)
			selected, err := selectRange(snapshot, measurement, scenario.retain)
			if err != nil {
				t.Fatal(err)
			}
			if (selected != nil) != scenario.selected {
				t.Fatalf("selection = %v", selected)
			}
			if selected != nil && (selected.Start != 0 || selected.End != scenario.end) {
				t.Fatalf("range = %+v", selected)
			}
		})
	}
	snapshot, measurement := selectionSnapshot(t, 4)
	snapshot.Surface.Nodes = []int64{2, 0, 3, 1}
	for index, sequence := range snapshot.Surface.Nodes {
		measurement.Nodes[index].Seq = sequence
	}
	selected, err := selectRange(snapshot, measurement, 15)
	if err != nil || !reflect.DeepEqual(selected, &compaction.SurfaceRange{
		Start: 2,
		End:   0,
	}) {
		t.Fatalf("non-monotonic Surface = %v, %v", selected, err)
	}
}

func TestSelectionRejectsMismatchedOrInvalidPrices(t *testing.T) {
	for _, invalid := range []string{"revision", "barrier", "length", "order", "negative prefix", "unsafe prefix", "tail overflow", "negative budget"} {
		t.Run(invalid, func(t *testing.T) {
			snapshot, measurement := selectionSnapshot(t, 4)
			retain := int64(0)
			switch invalid {
			case "revision":
				measurement.LogRevision--
			case "barrier":
				snapshot.Barrier.NextSeq++
			case "length":
				measurement.Nodes = measurement.Nodes[:3]
			case "order":
				measurement.Nodes[0].Seq = 1
			case "negative prefix":
				measurement.Nodes[0].Tokens = -1
			case "unsafe prefix":
				measurement.Nodes[0].Tokens = 1 << 53
			case "tail overflow":
				measurement.Nodes[3].Tokens = 1<<53 - 1
				retain = 1 << 53
			case "negative budget":
				retain = -1
			}
			if selected, err := selectRange(snapshot, measurement, retain); err == nil || selected != nil {
				t.Fatalf("invalid selection = %v, %v", selected, err)
			}
		})
	}
}

func TestSelectionKeepsToolCallAndResultTogether(t *testing.T) {
	snapshot, measurement := selectionSnapshot(t, 3)
	assistantValue, err := agentmessage.NewAssistantMessage(agentmessage.AssistantMessageInput{
		Content: []agentmessage.ContentBlock{
			agentmessage.ToolCallBlock{
				ID:        "call",
				Name:      "read",
				Arguments: `{}`,
			},
		},
		Source: agentmessage.ModelMessageSource{
			Provider: "test",
			Model:    "test",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(session.AssistantMessage{
		Turn:    1,
		Step:    1,
		Message: assistantValue,
	})
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Events[1].Type = session.AssistantMessageEventName
	snapshot.Events[1].Data = encoded
	resultValue, err := agentmessage.NewToolResultMessage(agentmessage.ToolResultMessageInput{
		CallID:  "call",
		Content: []agentmessage.ContentBlock{agentmessage.NewTextBlock("result")},
	})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err = json.Marshal(session.ToolResult{
		Turn:    1,
		Step:    1,
		Message: resultValue,
	})
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Events[2].Type = session.ToolResultEventName
	snapshot.Events[2].Data = encoded
	selected, err := selectRange(snapshot, measurement, 0)
	if err != nil || selected == nil || selected.Start != 0 || selected.End != 0 {
		t.Fatalf("paired tail = %v, %v", selected, err)
	}
}
