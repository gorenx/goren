package llm_test

import (
	"encoding/json"
	"testing"

	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/llm"
)

func TestBlockAssemblerInterleavedStream(t *testing.T) {
	t.Parallel()
	toolName := "echo"
	entries := []llm.StreamChunk{
		llm.BlockStartChunk{Index: 0, BlockType: "reasoning"},
		llm.ReasoningDeltaChunk{Index: 0, Text: "thinking…"},
		llm.BlockEndChunk{Index: 0, Block: agentmessage.ReasoningBlock{Text: "thinking…"}},
		llm.TextDeltaChunk{Index: 1, Text: "Hello"},
		llm.TextDeltaChunk{Index: 1, Text: " world"},
		llm.ToolCallDeltaChunk{Index: 2, ID: "call-1", Name: &toolName, ArgumentsDelta: `{"text":`},
		llm.ToolCallDeltaChunk{Index: 2, ID: "call-1", ArgumentsDelta: `"hi"}`},
		llm.UsageChunk{Usage: llm.TokenUsage{InputTokens: 10, OutputTokens: 5}},
		llm.FinishChunk{Reason: llm.ToolCallsFinish{}},
	}
	assembly := llm.NewBlockAssembler()
	for _, entry := range entries {
		if err := assembly.Push(entry); err != nil {
			t.Fatal(err)
		}
	}
	content, err := assembly.AssembledBlocks()
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(content)
	if err != nil {
		t.Fatal(err)
	}
	want := `[{"type":"reasoning","text":"thinking…"},{"type":"text","text":"Hello world"},{"type":"tool-call","id":"call-1","name":"echo","arguments":"{\"text\":\"hi\"}"}]`
	if string(encoded) != want {
		t.Fatalf("blocks = %s", encoded)
	}
	usage, found := assembly.UsageValue()
	if !found || usage.InputTokens != 10 || usage.OutputTokens != 5 {
		t.Fatalf("usage = (%#v, %t)", usage, found)
	}
	if assembly.FinishValue().ReasonKind() != "tool-calls" {
		t.Fatalf("finish = %q", assembly.FinishValue().ReasonKind())
	}
}

func TestBlockAssemblerFirstCloseWinsAndMaxTokensDropsToolCalls(t *testing.T) {
	t.Parallel()
	assembly := llm.NewBlockAssembler()
	for _, entry := range []llm.StreamChunk{
		llm.BlockEndChunk{Index: 0, Block: agentmessage.ReasoningBlock{Text: "first"}},
		llm.BlockEndChunk{Index: 0, Block: agentmessage.TextBlock{Text: "second"}},
		llm.ToolCallDeltaChunk{Index: 1, ID: "call-1", ArgumentsDelta: `{}`},
		llm.FinishChunk{Reason: llm.MaxTokensFinish{}},
	} {
		if err := assembly.Push(entry); err != nil {
			t.Fatal(err)
		}
	}
	content, err := assembly.AssembledBlocks()
	if err != nil {
		t.Fatal(err)
	}
	if len(content) != 1 || content[0].ContentType() != "reasoning" {
		t.Fatalf("blocks = %#v", content)
	}
}

func TestStreamChunkRoundTripRestoresNestedInterfaces(t *testing.T) {
	t.Parallel()
	source := llm.FinishChunk{Reason: llm.ErrorFinish{Failure: llm.LlmFailure{Message: "failed", Code: "SERVER"}}, ReplayState: json.RawMessage(`{"id":1}`)}
	encoded, err := json.Marshal(source)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := llm.DecodeStreamChunk(encoded)
	if err != nil {
		t.Fatal(err)
	}
	reencoded, err := json.Marshal(restored)
	if err != nil {
		t.Fatal(err)
	}
	if string(reencoded) != string(encoded) {
		t.Fatalf("round trip = %s, want %s", reencoded, encoded)
	}
}
