package toolresultpruner

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/compaction"
	"github.com/gorenx/goren/llm/tokenmeter"
	"github.com/gorenx/goren/session"
)

type pricingStub struct {
	price       int64
	failOnCall  int
	estimateHit int
}

func (*pricingStub) Measure(
	context.Context,
	session.Context,
	*session.EpochHeader,
) (tokenmeter.Measurement, error) {
	return tokenmeter.Measurement{}, nil
}

func (stub *pricingStub) EstimateMessage(agentmessage.Message) (int64, error) {
	stub.estimateHit++
	if stub.failOnCall != 0 && stub.estimateHit == stub.failOnCall {
		return 0, errors.New("price failed")
	}
	return stub.price, nil
}

func TestPruneContentUsesCodePointsAndPreservesRichBlockOrder(t *testing.T) {
	t.Parallel()
	implementation := newPrunerFixture(t, &pricingStub{}, Config{
		ThresholdChars: pointerToInt(50),
		HeadChars:      pointerToInt(4),
		TailChars:      pointerToInt(3),
	})
	measured, err := implementation.MeasureContent([]agentmessage.ContentBlock{
		agentmessage.NewTextBlock("a😀b"),
		agentmessage.ReasoningBlock{
			Type: "reasoning",
			Text: "not measured",
		},
	})
	if err != nil || measured != 3 {
		t.Fatalf("measured = %d, %v", measured, err)
	}

	reasoningValue := agentmessage.ReasoningBlock{
		Type: "reasoning",
		Text: "private-rich-block",
	}
	callValue := agentmessage.ToolCallBlock{
		Type:      "tool-call",
		ID:        "nested",
		Name:      "nested",
		Arguments: `{}`,
	}
	pruned, changed, err := implementation.PruneContent([]agentmessage.ContentBlock{
		agentmessage.NewTextBlock(strings.Repeat("A", 40)),
		reasoningValue,
		agentmessage.NewTextBlock(strings.Repeat("B", 30)),
		callValue,
		agentmessage.NewTextBlock(strings.Repeat("C", 30)),
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []agentmessage.ContentBlock{
		agentmessage.NewTextBlock("AAAA" + PruneMarker),
		reasoningValue,
		callValue,
		agentmessage.NewTextBlock("CCC"),
	}
	if !changed || !reflect.DeepEqual(pruned, want) {
		t.Fatalf("pruned = %#v, changed = %t", pruned, changed)
	}

	emojiBlocks, changed, err := implementation.PruneContent([]agentmessage.ContentBlock{
		agentmessage.NewTextBlock(strings.Repeat("😀", 60)),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !changed || len(emojiBlocks) != 1 {
		t.Fatalf("emoji prune = %#v, %t", emojiBlocks, changed)
	}
	emojiText := emojiBlocks[0].(agentmessage.TextBlock).Text
	if emojiText != strings.Repeat("😀", 4)+PruneMarker+strings.Repeat("😀", 3) ||
		strings.ContainsRune(emojiText, '\uFFFD') {
		t.Fatalf("emoji text = %q", emojiText)
	}
}

func TestPruneContentSkipsWithinBudgetAndPreservesOpaqueTextFields(t *testing.T) {
	t.Parallel()
	implementation := newPrunerFixture(t, &pricingStub{}, Config{
		ThresholdChars: pointerToInt(45),
		HeadChars:      pointerToInt(2),
		TailChars:      pointerToInt(1),
	})
	withinBudget := []agentmessage.ContentBlock{agentmessage.NewTextBlock("short")}
	pruned, changed, err := implementation.PruneContent(withinBudget)
	if err != nil || changed || pruned != nil {
		t.Fatalf("within budget = %#v, %t, %v", pruned, changed, err)
	}

	opaqueBlocks, err := agentmessage.DecodeContentBlocks(json.RawMessage(
		`[{"type":"text","text":"xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx","style":"kept"}]`,
	))
	if err != nil {
		t.Fatal(err)
	}
	pruned, changed, err = implementation.PruneContent(opaqueBlocks)
	if err != nil {
		t.Fatal(err)
	}
	if !changed || len(pruned) != 1 {
		t.Fatalf("opaque prune = %#v, %t", pruned, changed)
	}
	encoded, err := json.Marshal(pruned[0])
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatal(err)
	}
	var textValue string
	if err := json.Unmarshal(fields["text"], &textValue); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"style":"kept"`) ||
		!strings.Contains(textValue, PruneMarker) {
		t.Fatalf("opaque text = %s", encoded)
	}
}

func TestPruneSessionCommitsShadowPriceAndReplacementAdjacently(t *testing.T) {
	t.Parallel()
	conversation := newPrunerSession(t, "prune-session")
	metadata := json.RawMessage(`{"diff":["a","b"]}`)
	errorInfo := &session.ToolErrorInfo{
		Name: "ExitError",
		Code: "EXIT_1",
	}
	originalSeq, originalMessage := appendToolResultFixture(
		t,
		conversation,
		"call-1",
		[]agentmessage.ContentBlock{
			agentmessage.NewTextBlock(strings.Repeat("x", 100)),
		},
		true,
		errorInfo,
		metadata,
	)
	pricing := &pricingStub{
		price: 37,
	}
	implementation := newPrunerFixture(t, pricing, Config{
		ThresholdChars: pointerToInt(50),
		HeadChars:      pointerToInt(4),
		TailChars:      pointerToInt(3),
	})
	outcome, err := implementation.PruneSession(context.Background(), conversation)
	if err != nil {
		t.Fatal(err)
	}
	if len(outcome.Pruned) != 1 || outcome.CharsRemoved <= 0 {
		t.Fatalf("prune outcome = %#v", outcome)
	}
	landed := outcome.Pruned[0]
	if landed.OriginalSeq != originalSeq || landed.CallID != "call-1" ||
		landed.CharsBefore != 100 || landed.CharsAfter > 50 {
		t.Fatalf("landed replacement = %#v", landed)
	}
	entries := conversation.Events()
	if landed.ReplacementSeq <= 0 ||
		entries[landed.ReplacementSeq-1].Type != compaction.PruneEventName ||
		entries[landed.ReplacementSeq].Type != session.ToolResultEventName {
		t.Fatalf("replacement adjacency around seq %d = %#v", landed.ReplacementSeq, entries)
	}
	priceFact, err := compaction.DecodePrune(entries[landed.ReplacementSeq-1].Data)
	if err != nil {
		t.Fatal(err)
	}
	if priceFact.ShadowedTokenCount != 37 ||
		!reflect.DeepEqual(priceFact.ShadowedSeqs, []int64{originalSeq}) {
		t.Fatalf("shadow price = %#v", priceFact)
	}
	replacementEvent := entries[landed.ReplacementSeq]
	if replacementEvent.SurfaceOp == nil ||
		replacementEvent.SurfaceOp.Kind != session.SurfaceOperationReplace ||
		replacementEvent.SurfaceOp.Start != originalSeq ||
		replacementEvent.SourceEventSeqs == nil ||
		!reflect.DeepEqual(*replacementEvent.SourceEventSeqs, []int64{originalSeq}) {
		t.Fatalf("replacement event = %#v", replacementEvent)
	}
	replacementValue, err := session.DeriveEventMessage(replacementEvent)
	if err != nil {
		t.Fatal(err)
	}
	replacementMessage := replacementValue.(agentmessage.ToolResultMessage)
	if replacementMessage.StableID() != originalMessage.StableID() ||
		replacementMessage.SourceValue().(agentmessage.ToolMessageSource).CallID != "call-1" {
		t.Fatalf("replacement message identity = %#v", replacementMessage)
	}
	var replacementPayload struct {
		Error *session.ToolErrorInfo `json:"error"`
		Meta  json.RawMessage        `json:"meta"`
	}
	if err := json.Unmarshal(replacementEvent.Data, &replacementPayload); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(replacementPayload.Error, errorInfo) ||
		!jsonEqual(replacementPayload.Meta, metadata) {
		t.Fatalf("replacement payload = %#v", replacementPayload)
	}

	second, err := implementation.PruneSession(context.Background(), conversation)
	if err != nil || len(second.Pruned) != 0 || second.CharsRemoved != 0 {
		t.Fatalf("second pass = %#v, %v", second, err)
	}
	replayed, err := session.New("prune-replay", session.CreateOptions{
		Seed: conversation.Events(),
	})
	if err != nil {
		t.Fatal(err)
	}
	replayedMessages, err := replayed.DeriveMessages()
	if err != nil {
		t.Fatal(err)
	}
	currentMessages, err := conversation.DeriveMessages()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(replayedMessages, currentMessages) ||
		replayed.Surface().ReplaceGeneration != conversation.Surface().ReplaceGeneration {
		t.Fatalf("replay mismatch = %#v / %#v", replayedMessages, currentMessages)
	}
}

func TestPruneSessionCommitsNothingWhenPlanBuildFails(t *testing.T) {
	t.Parallel()
	conversation := newPrunerSession(t, "prune-partial")
	appendToolResultFixture(
		t,
		conversation,
		"call-a",
		[]agentmessage.ContentBlock{agentmessage.NewTextBlock(strings.Repeat("a", 100))},
		false,
		nil,
		nil,
	)
	appendToolResultFixture(
		t,
		conversation,
		"call-b",
		[]agentmessage.ContentBlock{agentmessage.NewTextBlock(strings.Repeat("b", 100))},
		false,
		nil,
		nil,
	)
	implementation := newPrunerFixture(t, &pricingStub{
		price:      10,
		failOnCall: 2,
	}, Config{
		ThresholdChars: pointerToInt(50),
		HeadChars:      pointerToInt(4),
		TailChars:      pointerToInt(3),
	})
	before := conversation.Snapshot()
	outcome, err := implementation.PruneSession(context.Background(), conversation)
	if err == nil || !strings.Contains(err.Error(), "price failed") {
		t.Fatalf("prune error = %v", err)
	}
	if len(outcome.Pruned) != 0 || outcome.CharsRemoved != 0 {
		t.Fatalf("failed pruning result = %#v", outcome)
	}
	after := conversation.Snapshot()
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("Session changed after failed plan: before = %#v, after = %#v", before, after)
	}
}

func TestPruneSessionPreservesMergeExtendedToolResultData(t *testing.T) {
	t.Parallel()
	sourceSession := newPrunerSession(t, "extended-source")
	appendToolResultFixture(
		t,
		sourceSession,
		"call-extended",
		[]agentmessage.ContentBlock{agentmessage.NewTextBlock(strings.Repeat("z", 100))},
		false,
		nil,
		nil,
	)
	seed := sourceSession.Events()
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(seed[0].Data, &fields); err != nil {
		t.Fatal(err)
	}
	fields["futureField"] = json.RawMessage(`{"nested":true}`)
	encoded, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	seed[0].Data = encoded
	conversation, err := session.New("extended-replay", session.CreateOptions{
		Seed: seed,
	})
	if err != nil {
		t.Fatal(err)
	}
	implementation := newPrunerFixture(t, &pricingStub{
		price: 10,
	}, Config{
		ThresholdChars: pointerToInt(50),
		HeadChars:      pointerToInt(4),
		TailChars:      pointerToInt(3),
	})
	outcome, err := implementation.PruneSession(context.Background(), conversation)
	if err != nil {
		t.Fatal(err)
	}
	if len(outcome.Pruned) != 1 {
		t.Fatalf("prune outcome = %#v", outcome)
	}
	replacementEvent := conversation.Events()[outcome.Pruned[0].ReplacementSeq]
	var replacementFields map[string]json.RawMessage
	if err := json.Unmarshal(replacementEvent.Data, &replacementFields); err != nil {
		t.Fatal(err)
	}
	if !jsonEqual(replacementFields["futureField"], fields["futureField"]) {
		t.Fatalf("future field = %s", replacementFields["futureField"])
	}
}

func TestResolveConfigUsesSourceDefaultsAndRejectsOversizedEmission(t *testing.T) {
	t.Parallel()
	resolved, err := ResolveConfig(Config{})
	if err != nil {
		t.Fatal(err)
	}
	if resolved.ThresholdChars != 8192 || resolved.HeadChars != 4096 ||
		resolved.TailChars != 1024 {
		t.Fatalf("defaults = %#v", resolved)
	}
	_, err = ResolveConfig(Config{
		ThresholdChars: pointerToInt(50),
		HeadChars:      pointerToInt(20),
		TailChars:      pointerToInt(20),
	})
	if err == nil || !strings.Contains(err.Error(), "exceed threshold") {
		t.Fatalf("budget error = %v", err)
	}
}

func newPrunerFixture(
	testingContext *testing.T,
	meterService tokenmeter.Meter,
	rawSettings Config,
) *ToolResultPruner {
	testingContext.Helper()
	resolved, err := ResolveConfig(rawSettings)
	if err != nil {
		testingContext.Fatal(err)
	}
	implementation := newToolResultPruner(resolved)
	implementation.bind(meterService)
	return implementation
}

func newPrunerSession(
	testingContext *testing.T,
	identifier session.SessionID,
) session.Context {
	testingContext.Helper()
	conversation, err := session.New(identifier, session.CreateOptions{})
	if err != nil {
		testingContext.Fatal(err)
	}
	return conversation
}

func appendToolResultFixture(
	testingContext *testing.T,
	conversation session.Context,
	callID agentmessage.CallID,
	content []agentmessage.ContentBlock,
	isError bool,
	errorInfo *session.ToolErrorInfo,
	metadata json.RawMessage,
) (int64, agentmessage.ToolResultMessage) {
	testingContext.Helper()
	messageValue, err := agentmessage.NewToolResultMessage(agentmessage.ToolResultMessageInput{
		CallID:  callID,
		Content: content,
		IsError: isError,
	})
	if err != nil {
		testingContext.Fatal(err)
	}
	committed, err := session.Event{}, error(nil)
	{
		var committedEvent session.Event
		var writeErr error
		draft, draftErr := session.NewSurfaceEventDraft(session.ToolResultAdded,
			session.ToolResult{
				Turn:    1,
				Step:    1,
				Message: messageValue,
				Error:   errorInfo,
				Meta:    metadata,
			},
			session.SurfaceIntent{
				Operation: session.SurfaceAppend(),
			})
		writeErr = draftErr
		if draftErr == nil {
			receipt, commitErr := conversation.Commit(context.Background(), session.Batch(draft))
			writeErr = commitErr
			if commitErr == nil {
				committedEvent = receipt.Events[0]
			}
		}
		committed = committedEvent
		err = writeErr
	}

	if err != nil {
		testingContext.Fatal(err)
	}
	return committed.Seq, messageValue
}

func pointerToInt(value int) *int {
	return &value
}

func jsonEqual(left json.RawMessage, right json.RawMessage) bool {
	var leftValue any
	var rightValue any
	return json.Unmarshal(left, &leftValue) == nil &&
		json.Unmarshal(right, &rightValue) == nil &&
		reflect.DeepEqual(leftValue, rightValue)
}
