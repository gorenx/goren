package tokenmeter

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/compaction"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/session"
	sessionprojection "github.com/gorenx/goren/session/projection"
)

func TestProjectionUnitsServeSourceCompatibleEmptyViews(t *testing.T) {
	t.Parallel()
	projections := registeredProjectionFixture(t)
	conversation := newConversation(t, "projection-empty")
	snapshot, err := projections.Snapshot(conversation)
	if err != nil {
		t.Fatal(err)
	}
	assertProjectionValue(
		t,
		snapshot.Values,
		TokenUsageProjectionKey,
		TokenUsageProjection{},
	)
	assertProjectionValue(
		t,
		snapshot.Values,
		ContextPressureProjectionKey,
		ContextPressureProjection{},
	)
	assertProjectionValue(
		t,
		snapshot.Values,
		ContextBreakdownProjectionKey,
		ContextBreakdownProjection{},
	)
}

func TestTokenUsageProjectionReplacesSameStepSample(t *testing.T) {
	t.Parallel()
	projections := registeredProjectionFixture(t)
	conversation := newConversation(t, "usage-projection")
	{
		draft, err := session.NewEventDraft(session.StepStarted,
			session.StepPosition{
				Turn: 1,
				Step: 1,
			})
		if err == nil {
			_, err = conversation.Commit(context.Background(), session.Batch(draft))
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	firstUsage := llm.TokenUsage{
		InputTokens:  10,
		OutputTokens: 2,
	}
	appendUsageChunk(t, conversation, firstUsage)
	firstSnapshot, err := projections.Snapshot(conversation)
	if err != nil {
		t.Fatal(err)
	}
	assertProjectionValue(
		t,
		firstSnapshot.Values,
		TokenUsageProjectionKey,
		TokenUsageProjection{
			UncachedInputTokens: 10,
			OutputTokens:        2,
		},
	)
	finalUsage := llm.TokenUsage{
		InputTokens:  12,
		OutputTokens: 3,
	}
	emptySources := []int64{}
	appendAssistantWithSources(
		t,
		conversation,
		"answer",
		&finalUsage,
		&emptySources,
	)
	finalSnapshot, err := projections.Snapshot(conversation)
	if err != nil {
		t.Fatal(err)
	}
	assertProjectionValue(
		t,
		finalSnapshot.Values,
		TokenUsageProjectionKey,
		TokenUsageProjection{
			UncachedInputTokens: 12,
			OutputTokens:        3,
		},
	)
}

func TestPressureAndBreakdownTrackSurfaceAndMeteredCompaction(t *testing.T) {
	t.Parallel()
	projections := registeredProjectionFixture(t)
	conversation := newConversation(t, "surface-projections")
	contextWindow := 64_000
	{
		draft, err := session.NewEventDraft(session.RequestContextSet,
			session.RequestRouteContext{
				Provider:      "mock",
				Model:         "model-a",
				ContextWindow: &contextWindow,
			})
		if err == nil {
			_, err = conversation.Commit(context.Background(), session.Batch(draft))
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	firstSeq := appendUser(t, conversation, "abcd")
	{
		draft, err := session.NewEventDraft(session.StepStarted,
			session.StepPosition{
				Turn: 1,
				Step: 1,
			})
		if err == nil {
			_, err = conversation.Commit(context.Background(), session.Batch(draft))
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	usageValue := llm.TokenUsage{
		InputTokens:  100,
		OutputTokens: 20,
	}
	appendUsageChunk(t, conversation, usageValue)
	emptySources := []int64{}
	appendAssistantWithSources(
		t,
		conversation,
		"answer",
		nil,
		&emptySources,
	)
	{
		draft, err := session.NewEventDraft(session.StepEnded,
			session.StepPosition{
				Turn: 1,
				Step: 1,
			})
		if err == nil {
			_, err = conversation.Commit(context.Background(), session.Batch(draft))
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	lastSeq := appendUser(t, conversation, "a follow-up that grows the surface")

	before, err := projections.Snapshot(conversation)
	if err != nil {
		t.Fatal(err)
	}
	var pressureBefore ContextPressureProjection
	decodeProjectionValue(t, before.Values, ContextPressureProjectionKey, &pressureBefore)
	if pressureBefore.PressureTokens == nil || *pressureBefore.PressureTokens != 100 ||
		pressureBefore.ProjectedTokens == nil || *pressureBefore.ProjectedTokens <= 100 ||
		pressureBefore.ContextWindow == nil || *pressureBefore.ContextWindow != 64_000 {
		t.Fatalf("pressure before compaction = %#v", pressureBefore)
	}

	meterService := newTokenMeter()
	measuredBefore, err := meterService.Measure(context.Background(), conversation, nil)
	if err != nil {
		t.Fatal(err)
	}
	shadowedSeqs, shadowedTokens := pricedRange(
		t,
		measuredBefore.Nodes,
		firstSeq,
		lastSeq,
	)
	transactionID := compaction.ID("projection-compaction")
	{
		draft, err := session.NewEventDraft(compaction.SummaryEvent,
			compaction.Summary{
				CompactionID: transactionID,
				Summary:      []agentmessage.ContentBlock{agentmessage.NewTextBlock("summary")},
				ShadowedRange: compaction.SurfaceRange{
					Start: firstSeq,
					End:   lastSeq,
				},
				ShadowedSeqs:       shadowedSeqs,
				ShadowedTokenCount: shadowedTokens,
				Provider:           "mock",
				Model:              "model-a",
			})
		if err == nil {
			_, err = conversation.Commit(context.Background(), session.Batch(draft))
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	replacement, err := agentmessage.NewUserMessage(agentmessage.UserMessageInput{
		Content: []agentmessage.ContentBlock{
			agentmessage.NewTextBlock("summary"),
		},
		Source: agentmessage.PluginMessageSource{
			Plugin: "tokenmeter-test",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	{
		draft, err := session.NewSurfaceEventDraft(session.UserMessageAdded,
			replacement,
			session.SurfaceIntent{
				Operation:       session.SurfaceReplace(firstSeq, lastSeq),
				SourceEventSeqs: &shadowedSeqs,
			})
		if err == nil {
			_, err = conversation.Commit(context.Background(), session.Batch(draft))
		}
		if err != nil {
			t.Fatal(err)
		}
	}

	after, err := projections.Snapshot(conversation)
	if err != nil {
		t.Fatal(err)
	}
	var pressureAfter ContextPressureProjection
	decodeProjectionValue(t, after.Values, ContextPressureProjectionKey, &pressureAfter)
	if pressureAfter.PressureTokens == nil || *pressureAfter.PressureTokens != 100 ||
		pressureAfter.ProjectedTokens == nil ||
		*pressureAfter.ProjectedTokens >= *pressureBefore.ProjectedTokens {
		t.Fatalf("pressure after compaction = %#v", pressureAfter)
	}
	var breakdown ContextBreakdownProjection
	decodeProjectionValue(t, after.Values, ContextBreakdownProjectionKey, &breakdown)
	measuredAfter, err := meterService.Measure(context.Background(), conversation, nil)
	if err != nil {
		t.Fatal(err)
	}
	if breakdown.MessageTokens != measuredAfter.SurfaceTokens {
		t.Fatalf(
			"breakdown message tokens = %d, meter surface tokens = %d",
			breakdown.MessageTokens,
			measuredAfter.SurfaceTokens,
		)
	}
}

func TestSurfaceProjectionRejectsAdjacentMismatchedClaim(t *testing.T) {
	t.Parallel()
	projections := registeredProjectionFixture(t)
	conversation := newConversation(t, "mismatched-claim")
	firstSeq := appendUser(t, conversation, "first")
	lastSeq := appendUser(t, conversation, "last")
	{
		draft, err := session.NewEventDraft(compaction.PruneEvent,
			compaction.Prune{
				ShadowedRange: compaction.SurfaceRange{
					Start: firstSeq,
					End:   firstSeq,
				},
				ShadowedSeqs:       []int64{firstSeq},
				ShadowedTokenCount: 1,
			})
		if err == nil {
			_, err = conversation.Commit(context.Background(), session.Batch(draft))
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	replacement := mustUserMessage(
		t,
		[]agentmessage.ContentBlock{
			agentmessage.NewTextBlock("replacement"),
		},
	)
	sources := []int64{firstSeq, lastSeq}
	{
		draft, err := session.NewSurfaceEventDraft(session.UserMessageAdded,
			replacement,
			session.SurfaceIntent{
				Operation:       session.SurfaceReplace(firstSeq, lastSeq),
				SourceEventSeqs: &sources,
			})
		if err == nil {
			_, err = conversation.Commit(context.Background(), session.Batch(draft))
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := projections.Snapshot(conversation); err == nil ||
		!strings.Contains(err.Error(), "no adjacent shadow price") {
		t.Fatalf("snapshot error = %v", err)
	}
}

func TestProjectionCheckpointRemainsBoundedAndRestoresViews(t *testing.T) {
	t.Parallel()
	projections := registeredProjectionFixture(t)
	conversation := newConversation(t, "projection-checkpoint")
	for index := 0; index < 32; index++ {
		appendUser(t, conversation, "message")
	}
	rows, err := projections.Checkpoint(conversation)
	if err != nil {
		t.Fatal(err)
	}
	breakdownRow := rows[ContextBreakdownProjectionKey]
	var stateFields map[string]json.RawMessage
	if err := json.Unmarshal(breakdownRow.Value, &stateFields); err != nil {
		t.Fatal(err)
	}
	wantKeys := []string{"messageTokens", "systemTokens", "toolsTokens"}
	gotKeys := make([]string, 0, len(stateFields))
	for fieldName := range stateFields {
		gotKeys = append(gotKeys, fieldName)
	}
	if !sameStringSet(gotKeys, wantKeys) {
		t.Fatalf("breakdown checkpoint keys = %v", gotKeys)
	}
	encoded, err := json.Marshal(rows)
	if err != nil {
		t.Fatal(err)
	}
	var restoredRows sessionprojection.Checkpoint
	if err := json.Unmarshal(encoded, &restoredRows); err != nil {
		t.Fatal(err)
	}
	restoredRegistry := registeredProjectionFixture(t)
	values, err := restoredRegistry.ViewCheckpoint(restoredRows)
	if err != nil {
		t.Fatal(err)
	}
	var restored ContextBreakdownProjection
	decodeProjectionValue(t, values, ContextBreakdownProjectionKey, &restored)
	if restored.MessageTokens == 0 {
		t.Fatalf("restored breakdown = %#v", restored)
	}
}

func registeredProjectionFixture(testingContext *testing.T) *sessionprojection.DriveRegistry {
	testingContext.Helper()
	projections := sessionprojection.NewDriveRegistry()
	units := []sessionprojection.Unit{
		tokenUsageUnit{},
		contextPressureUnit{},
		contextBreakdownUnit{},
	}
	for _, unitValue := range units {
		if _, err := projections.Register(unitValue); err != nil {
			testingContext.Fatal(err)
		}
	}
	return projections
}

func appendUsageChunk(
	testingContext *testing.T,
	conversation session.Context,
	usageValue llm.TokenUsage,
) {
	testingContext.Helper()
	{
		draft, err := session.NewEventDraft(session.AssistantChunked,
			session.AssistantChunk{
				Turn: 1,
				Step: 1,
				Chunk: llm.UsageChunk{
					Usage: usageValue,
				},
			})
		if err == nil {
			_, err = conversation.Commit(context.Background(), session.Batch(draft))
		}
		if err != nil {
			testingContext.Fatal(err)
		}
	}
}

func pricedRange(
	testingContext *testing.T,
	nodes []SurfaceNode,
	startSeq int64,
	endSeq int64,
) ([]int64, int64) {
	testingContext.Helper()
	startIndex := -1
	endIndex := -1
	for index, nodeValue := range nodes {
		if nodeValue.Seq == startSeq {
			startIndex = index
		}
		if nodeValue.Seq == endSeq {
			endIndex = index
		}
	}
	if startIndex < 0 || endIndex < startIndex {
		testingContext.Fatalf("range %d-%d is absent from %#v", startSeq, endSeq, nodes)
	}
	selected := nodes[startIndex : endIndex+1]
	sequences := make([]int64, len(selected))
	var total int64
	for index, nodeValue := range selected {
		sequences[index] = nodeValue.Seq
		total += nodeValue.Tokens
	}
	return sequences, total
}

func assertProjectionValue[T any](
	testingContext *testing.T,
	values sessionprojection.Values,
	projectionKey string,
	want T,
) {
	testingContext.Helper()
	var got T
	decodeProjectionValue(testingContext, values, projectionKey, &got)
	if !reflect.DeepEqual(got, want) {
		testingContext.Fatalf("projection %q = %#v, want %#v", projectionKey, got, want)
	}
}

func decodeProjectionValue(
	testingContext *testing.T,
	values sessionprojection.Values,
	projectionKey string,
	target any,
) {
	testingContext.Helper()
	rawValue, found := values[projectionKey]
	if !found {
		testingContext.Fatalf("projection %q is not registered", projectionKey)
	}
	if err := json.Unmarshal(rawValue, target); err != nil {
		testingContext.Fatal(err)
	}
}

func sameStringSet(left []string, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	counts := make(map[string]int, len(left))
	for _, value := range left {
		counts[value]++
	}
	for _, value := range right {
		counts[value]--
	}
	for _, count := range counts {
		if count != 0 {
			return false
		}
	}
	return true
}
