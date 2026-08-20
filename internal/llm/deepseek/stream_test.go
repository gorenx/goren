package deepseek

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/gorenx/goren/llm"
)

func TestSSEParserHandlesFramingAndRequiresDone(t *testing.T) {
	t.Parallel()
	comments := make([]string, 0)
	payloadFlow, err := newSSEPayloadStream(io.NopCloser(strings.NewReader(
		"\ufeff: ready\r\n\r\ndata: first\r\ndata: second\r\n\r\nevent: ignored\r\ndata: [DONE]\r\n\r\n",
	)), time.Second, func(comment string) { comments = append(comments, comment) })
	if err != nil {
		t.Fatal(err)
	}
	first, available, err := payloadFlow.NextPayload(context.Background())
	if err != nil || !available || first != "first\nsecond" {
		t.Fatalf("first payload = (%q, %t, %v)", first, available, err)
	}
	last, available, err := payloadFlow.NextPayload(context.Background())
	if err != nil || !available || last != donePayload || len(comments) != 1 || comments[0] != "ready" {
		t.Fatalf("last payload = (%q, %t, %v), comments=%#v", last, available, err, comments)
	}
	_, available, err = payloadFlow.NextPayload(context.Background())
	if err != nil || available {
		t.Fatalf("post-DONE = (%t, %v)", available, err)
	}

	truncated, err := newSSEPayloadStream(io.NopCloser(strings.NewReader("data: [DONE]")), time.Second, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = truncated.NextPayload(context.Background())
	var providerFailure *llm.LlmError
	if !errors.As(err, &providerFailure) || providerFailure.Code() != "STREAM_CLOSED" {
		t.Fatalf("truncated error = %#v", err)
	}
}

func TestSSEParserIdleTimeoutClosesBody(t *testing.T) {
	t.Parallel()
	reader, writer := io.Pipe()
	payloadFlow, err := newSSEPayloadStream(reader, 20*time.Millisecond, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = payloadFlow.NextPayload(context.Background())
	var providerFailure *llm.LlmError
	if !errors.As(err, &providerFailure) || providerFailure.Code() != "TIMEOUT" {
		t.Fatalf("timeout error = %#v", err)
	}
	if _, writeErr := writer.Write([]byte("data: late\n\n")); writeErr == nil {
		t.Fatal("timed-out body remained writable")
	}
	_ = writer.Close()
}

func TestTranslateAssemblesReasoningTextToolsUsageAndFinish(t *testing.T) {
	t.Parallel()
	payloads := &slicePayloadStream{
		payloads: []string{
			`{"choices":[{"delta":{"reasoning_content":""}}]}`,
			`{"choices":[{"delta":{"reasoning_content":"think"}}]}`,
			`{"choices":[{"delta":{"content":"answer"}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call-1","function":{"name":"lookup","arguments":"{\"q\":"}}]}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"x\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":10,"completion_tokens":4,"prompt_cache_hit_tokens":7,"completion_tokens_details":{"reasoning_tokens":2}}}`,
			donePayload,
		},
	}
	chunkFlow, err := translatePayloads(payloads)
	if err != nil {
		t.Fatal(err)
	}
	assembler := llm.NewBlockAssembler()
	kinds := make([]string, 0)
	for {
		entry, available, nextErr := chunkFlow.Next(context.Background())
		if nextErr != nil {
			t.Fatal(nextErr)
		}
		if !available {
			break
		}
		kinds = append(kinds, entry.ChunkType())
		if err := assembler.Push(entry); err != nil {
			t.Fatal(err)
		}
	}
	blocks, err := assembler.AssembledBlocks()
	if err != nil {
		t.Fatal(err)
	}
	if len(blocks) != 3 || blocks[0].ContentType() != "reasoning" || blocks[1].ContentType() != "text" || blocks[2].ContentType() != "tool-call" {
		t.Fatalf("assembled blocks = %#v; chunks=%#v", blocks, kinds)
	}
	usage, found := assembler.UsageValue()
	if !found || usage.InputTokens != 3 || usage.OutputTokens != 4 || usage.CacheReadTokens == nil || *usage.CacheReadTokens != 7 ||
		usage.ReasoningTokens == nil || *usage.ReasoningTokens != 2 || assembler.FinishValue().ReasonKind() != "tool-calls" {
		t.Fatalf("assembly metadata = (%#v, %t, %#v)", usage, found, assembler.FinishValue())
	}
}

func TestTranslateClassifiesEmptyAndMalformedResponses(t *testing.T) {
	t.Parallel()
	chunkFlow, err := translatePayloads(&slicePayloadStream{
		payloads: []string{donePayload},
	})
	if err != nil {
		t.Fatal(err)
	}
	entry, available, err := chunkFlow.Next(context.Background())
	if err != nil || !available {
		t.Fatalf("empty finish = (%#v, %t, %v)", entry, available, err)
	}
	finish := entry.(llm.FinishChunk)
	errorReason := finish.Reason.(llm.ErrorFinish)
	if errorReason.Failure.Code != llm.EmptyResponseCode {
		t.Fatalf("empty response finish = %#v", finish)
	}

	malformed, err := translatePayloads(&slicePayloadStream{
		payloads: []string{"{bad"},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = malformed.Next(context.Background())
	var providerFailure *llm.LlmError
	if !errors.As(err, &providerFailure) || providerFailure.Code() != "MALFORMED_RESPONSE" {
		t.Fatalf("malformed error = %#v", err)
	}
}

type slicePayloadStream struct {
	payloads []string
	index    int
	closed   bool
}

func (streamState *slicePayloadStream) NextPayload(context.Context) (string, bool, error) {
	if streamState.closed || streamState.index >= len(streamState.payloads) {
		return "", false, nil
	}
	payload := streamState.payloads[streamState.index]
	streamState.index++
	return payload, true, nil
}

func (streamState *slicePayloadStream) Close(context.Context) error {
	streamState.closed = true
	return nil
}
