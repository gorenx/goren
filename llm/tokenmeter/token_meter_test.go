package tokenmeter

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/session"
)

func TestTokenMeterMeasuresEmptyAndDetachedSurface(t *testing.T) {
	t.Parallel()
	conversation := newConversation(t, "empty")
	meterService := newTokenMeter()
	empty, err := meterService.Measure(context.Background(), conversation, nil)
	if err != nil {
		t.Fatal(err)
	}
	if empty.LogRevision != 0 || empty.Baseline.Kind != BaselineNone ||
		empty.TotalTokens != 0 || empty.SurfaceTokens != 0 || len(empty.Nodes) != 0 {
		t.Fatalf("empty measurement = %#v", empty)
	}
	appendUser(t, conversation, "first")
	first, err := meterService.Measure(context.Background(), conversation, nil)
	if err != nil {
		t.Fatal(err)
	}
	firstSnapshot := append([]SurfaceNode(nil), first.Nodes...)
	appendUser(t, conversation, "second")
	second, err := meterService.Measure(context.Background(), conversation, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Nodes) != 1 || !reflect.DeepEqual(first.Nodes, firstSnapshot) {
		t.Fatalf("first measurement mutated = %#v", first)
	}
	if second.LogRevision != 2 || len(second.Nodes) != 2 ||
		sumNodeTokens(second.Nodes) != second.SurfaceTokens {
		t.Fatalf("advanced measurement = %#v", second)
	}
}

func TestTokenMeterPricesHeaderAndRequestOverrideWithoutChangingSurface(t *testing.T) {
	t.Parallel()
	conversation := newConversation(t, "header")
	appendUser(t, conversation, "question")
	prompt := "system"
	headerValue := fixtureHeader("model-a")
	headerValue.System = &prompt
	headerValue.Tools = []llm.ToolSchema{
		{
			Name:        "read",
			Description: "read",
			Parameters:  []byte(`{"type":"object"}`),
		},
	}
	{
		draft, err := session.NewEventDraft(session.RequestHeaderSet,
			session.RequestHeaderSnapshot{
				Header: headerValue,
				Reason: session.RequestHeaderInitial,
			})
		if err == nil {
			_, err = conversation.Commit(context.Background(), session.Batch(draft))
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	meterService := newTokenMeter()
	logged, err := meterService.Measure(context.Background(), conversation, nil)
	if err != nil {
		t.Fatal(err)
	}
	largePrompt := strings.Repeat("large override ", 100)
	override := fixtureHeader("model-b")
	override.System = &largePrompt
	overridden, err := meterService.Measure(context.Background(), conversation, &override)
	if err != nil {
		t.Fatal(err)
	}
	if logged.Baseline.Kind != BaselineEstimated ||
		overridden.TotalTokens <= logged.TotalTokens ||
		overridden.SurfaceTokens != logged.SurfaceTokens ||
		!reflect.DeepEqual(overridden.Nodes, logged.Nodes) {
		t.Fatalf("logged = %#v, overridden = %#v", logged, overridden)
	}
}

func TestTokenMeterUsesUsageAnchorAndDurableRewriteDelta(t *testing.T) {
	t.Parallel()
	conversation := newConversation(t, "usage-anchor")
	appendUser(t, conversation, "before")
	usageValue := llm.TokenUsage{
		InputTokens:  20,
		OutputTokens: 7,
	}
	cacheRead := int64(3)
	cacheWrite := int64(4)
	usageValue.CacheReadTokens = &cacheRead
	usageValue.CacheWriteTokens = &cacheWrite
	appendSuccessfulCall(
		t,
		conversation,
		fixtureHeader("model-a"),
		"short",
		"a much longer rewritten durable assistant answer",
		&usageValue,
	)
	meterService := newTokenMeter()
	measured, err := meterService.Measure(context.Background(), conversation, nil)
	if err != nil {
		t.Fatal(err)
	}
	if measured.Baseline.Kind != BaselineUsage || measured.Baseline.Tokens != 34 ||
		measured.SurfaceDeltaTokens <= 0 ||
		measured.TotalTokens != 34+measured.SurfaceDeltaTokens {
		t.Fatalf("measurement = %#v", measured)
	}
	if measured.Baseline.Usage == nil {
		t.Fatal("usage baseline omitted provider usage")
	}
	measured.Baseline.Usage.InputTokens = 1
	again, err := meterService.Measure(context.Background(), conversation, nil)
	if err != nil {
		t.Fatal(err)
	}
	if again.Baseline.Usage == nil || again.Baseline.Usage.InputTokens != 20 {
		t.Fatalf("measurement aliases Session usage = %#v", again.Baseline.Usage)
	}
}

func TestTokenMeterTreatsExplicitEmptyAndAbsentProvenanceDifferently(t *testing.T) {
	t.Parallel()
	usageValue := llm.TokenUsage{
		InputTokens:  20,
		OutputTokens: 20,
	}
	explicit := newConversation(t, "explicit")
	appendCallWithProvenance(
		t,
		explicit,
		fixtureHeader("model-a"),
		"listener injected text",
		&usageValue,
		pointerToSeqs([]int64{}),
	)
	legacy := newConversation(t, "legacy")
	appendCallWithProvenance(
		t,
		legacy,
		fixtureHeader("model-a"),
		"listener injected text",
		&usageValue,
		nil,
	)
	meterService := newTokenMeter()
	explicitMeasurement, err := meterService.Measure(context.Background(), explicit, nil)
	if err != nil {
		t.Fatal(err)
	}
	legacyMeasurement, err := meterService.Measure(context.Background(), legacy, nil)
	if err != nil {
		t.Fatal(err)
	}
	if explicitMeasurement.SurfaceDeltaTokens <= 0 || legacyMeasurement.SurfaceDeltaTokens != 0 {
		t.Fatalf(
			"explicit delta = %d, legacy delta = %d",
			explicitMeasurement.SurfaceDeltaTokens,
			legacyMeasurement.SurfaceDeltaTokens,
		)
	}
}

func TestTokenMeterRejectsMalformedStepTransactionally(t *testing.T) {
	t.Parallel()
	conversation := newConversation(t, "bad-step")
	assistantValue := newAssistantMessage(t, "bad")
	emptySources := []int64{}
	{
		draft, err := session.NewSurfaceEventDraft(session.AssistantMessaged,
			session.AssistantMessage{
				Turn:    1,
				Step:    1,
				Message: assistantValue,
			},
			session.SurfaceIntent{
				Operation:       session.SurfaceAppend(),
				SourceEventSeqs: &emptySources,
			})
		if err == nil {
			_, err = conversation.Commit(context.Background(), session.Batch(draft))
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	meterService := newTokenMeter()
	for attempt := 0; attempt < 2; attempt++ {
		_, err := meterService.Measure(context.Background(), conversation, nil)
		if err == nil || !strings.Contains(err.Error(), "no matching step/start") {
			t.Fatalf("attempt %d error = %v", attempt, err)
		}
	}
}

func TestTokenMeterFallsBackWhenProviderUsageUndercutsHeuristicAnchor(t *testing.T) {
	t.Parallel()
	conversation := newConversation(t, "low-usage")
	prompt := strings.Repeat("system context ", 128)
	headerValue := fixtureHeader("model-a")
	headerValue.System = &prompt
	usageValue := llm.TokenUsage{
		InputTokens:  1,
		OutputTokens: 1,
	}
	appendSuccessfulCall(
		t,
		conversation,
		headerValue,
		strings.Repeat("provider answer ", 256),
		strings.Repeat("provider answer ", 256),
		&usageValue,
	)
	measured, err := newTokenMeter().Measure(context.Background(), conversation, nil)
	if err != nil {
		t.Fatal(err)
	}
	if measured.Baseline.Kind != BaselineEstimated || measured.Baseline.Tokens <= 2 {
		t.Fatalf("low-usage baseline = %#v", measured.Baseline)
	}
}

func TestTokenMeterReusesUsageOnlyForMatchingCanonicalHeader(t *testing.T) {
	t.Parallel()
	conversation := newConversation(t, "header-anchor")
	headerValue := fixtureHeader("model-a")
	usageValue := llm.TokenUsage{
		InputTokens:  100,
		OutputTokens: 50,
	}
	appendSuccessfulCall(
		t,
		conversation,
		headerValue,
		"answer",
		"answer",
		&usageValue,
	)
	meterService := newTokenMeter()
	matching, err := meterService.Measure(context.Background(), conversation, &headerValue)
	if err != nil {
		t.Fatal(err)
	}
	if matching.Baseline.Kind != BaselineUsage {
		t.Fatalf("matching baseline = %#v", matching.Baseline)
	}
	changedPrompt := "different"
	changed := fixtureHeader("model-a")
	changed.System = &changedPrompt
	nonmatching, err := meterService.Measure(context.Background(), conversation, &changed)
	if err != nil {
		t.Fatal(err)
	}
	if nonmatching.Baseline.Kind != BaselineEstimated || nonmatching.SurfaceDeltaTokens != 0 {
		t.Fatalf("nonmatching baseline = %#v", nonmatching)
	}
}

func TestTokenMeterPricesEmptyAssistantSurfaceNodeAtZero(t *testing.T) {
	t.Parallel()
	conversation := newConversation(t, "empty-assistant")
	appendSuccessfulCall(
		t,
		conversation,
		fixtureHeader("model-a"),
		"",
		"",
		nil,
	)
	measured, err := newTokenMeter().Measure(context.Background(), conversation, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(measured.Nodes) != 1 || measured.Nodes[0].Tokens != 0 ||
		measured.SurfaceTokens != 0 {
		t.Fatalf("empty assistant measurement = %#v", measured)
	}
}

func TestTokenMeterRejectsOverlappingAndMismatchedStepBoundaries(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name      string
		populate  func(*testing.T, session.Context)
		wantError string
	}{
		{
			name: "overlapping-start",
			populate: func(testingContext *testing.T, conversation session.Context) {
				for _, position := range []session.StepPosition{
					{
						Turn: 1,
						Step: 1,
					},
					{
						Turn: 1,
						Step: 2,
					},
				} {
					{
						draft, err := session.NewEventDraft(session.StepStarted,
							position)
						if err == nil {
							_, err = conversation.Commit(context.Background(), session.Batch(draft))
						}
						if err != nil {
							testingContext.Fatal(err)
						}
					}
				}
			},
			wantError: "arrived before turn 1/step 1 ended",
		},
		{
			name: "mismatched-end",
			populate: func(testingContext *testing.T, conversation session.Context) {
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
						testingContext.Fatal(err)
					}
				}
				{
					draft, err := session.NewEventDraft(session.StepEnded,
						session.StepPosition{
							Turn: 1,
							Step: 2,
						})
					if err == nil {
						_, err = conversation.Commit(context.Background(), session.Batch(draft))
					}
					if err != nil {
						testingContext.Fatal(err)
					}
				}
			},
			wantError: "has no matching step/start event",
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			conversation := newConversation(t, session.SessionID(testCase.name))
			testCase.populate(t, conversation)
			meterService := newTokenMeter()
			for attempt := 0; attempt < 2; attempt++ {
				_, err := meterService.Measure(context.Background(), conversation, nil)
				if err == nil || !strings.Contains(err.Error(), testCase.wantError) {
					t.Fatalf("attempt %d error = %v", attempt, err)
				}
			}
		})
	}
}

func TestTokenMeterEagerlyAdvancesOnlyAllocatedFoldsAndDisposesThem(t *testing.T) {
	t.Parallel()
	allocated := newConversation(t, "allocated")
	unread := newConversation(t, "unread")
	meterService := newTokenMeter()
	if _, err := meterService.Measure(context.Background(), allocated, nil); err != nil {
		t.Fatal(err)
	}
	allocatedEvent := appendUser(t, allocated, "allocated event")
	unreadEvent := appendUser(t, unread, "unread event")
	if err := meterService.observeEvent(
		context.Background(),
		session.EventAppended{
			Conversation: allocated,
			Committed:    allocated.Events()[allocatedEvent],
		},
	); err != nil {
		t.Fatal(err)
	}
	if err := meterService.observeEvent(
		context.Background(),
		session.EventAppended{
			Conversation: unread,
			Committed:    unread.Events()[unreadEvent],
		},
	); err != nil {
		t.Fatal(err)
	}
	if meterService.folds[allocated].consumedEvents != 1 {
		t.Fatalf("allocated fold = %#v", meterService.folds[allocated])
	}
	if _, found := meterService.folds[unread]; found {
		t.Fatal("eager observation allocated an unread Session fold")
	}
	if err := meterService.observeEvent(
		context.Background(),
		session.Disposed{
			Conversation: allocated,
		},
	); err != nil {
		t.Fatal(err)
	}
	if _, found := meterService.folds[allocated]; found {
		t.Fatal("disposed Session retained its replay fold")
	}
}

func newConversation(testingContext *testing.T, identifier session.SessionID) session.Context {
	testingContext.Helper()
	conversation, err := session.New(identifier, session.CreateOptions{})
	if err != nil {
		testingContext.Fatal(err)
	}
	return conversation
}

func fixtureHeader(modelID string) session.EpochHeader {
	return session.CanonicalEpochHeader(session.EpochHeader{
		Config: llm.CallConfig{
			Provider: "mock",
			Model:    modelID,
		},
	})
}

func appendUser(
	testingContext *testing.T,
	conversation session.Context,
	textValue string,
) int64 {
	testingContext.Helper()
	messageValue := mustUserMessage(
		testingContext,
		[]llm.ContentBlock{
			llm.NewTextBlock(textValue),
		},
	)
	draft, err := session.NewSurfaceEventDraft(session.UserMessageAdded,
		messageValue,
		session.SurfaceIntent{
			Operation: session.SurfaceAppend(),
		})
	if err != nil {
		testingContext.Fatal(err)
	}
	receipt, err := conversation.Commit(context.Background(), session.Batch(draft))
	if err != nil {
		testingContext.Fatal(err)
	}
	return receipt.Events[0].Seq
}

func appendSuccessfulCall(
	testingContext *testing.T,
	conversation session.Context,
	headerValue session.EpochHeader,
	providerText string,
	durableText string,
	usageValue *llm.TokenUsage,
) {
	testingContext.Helper()
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
			testingContext.Fatal(err)
		}
	}
	{
		draft, err := session.NewEventDraft(session.RequestHeaderSet,
			session.RequestHeaderSnapshot{
				Header: headerValue,
				Reason: session.RequestHeaderInitial,
			})
		if err == nil {
			_, err = conversation.Commit(context.Background(), session.Batch(draft))
		}
		if err != nil {
			testingContext.Fatal(err)
		}
	}
	sources := make([]int64, 0, 3)
	chunks := []llm.StreamChunk{
		llm.BlockStartChunk{
			Index:     0,
			BlockType: "text",
		},
		llm.TextDeltaChunk{
			Index: 0,
			Text:  providerText,
		},
		llm.BlockEndChunk{
			Index: 0,
			Block: llm.NewTextBlock(providerText),
		},
	}
	for _, chunkValue := range chunks {
		committed, err := session.Event{}, error(nil)
		{
			var committedEvent session.Event
			var writeErr error
			draft, draftErr := session.NewEventDraft(session.AssistantChunked,
				session.AssistantChunk{
					Turn:  1,
					Step:  1,
					Chunk: chunkValue,
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
		sources = append(sources, committed.Seq)
	}
	appendAssistantWithSources(
		testingContext,
		conversation,
		durableText,
		usageValue,
		&sources,
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
			testingContext.Fatal(err)
		}
	}
}

func appendCallWithProvenance(
	testingContext *testing.T,
	conversation session.Context,
	headerValue session.EpochHeader,
	durableText string,
	usageValue *llm.TokenUsage,
	sourceSeqs *[]int64,
) {
	testingContext.Helper()
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
			testingContext.Fatal(err)
		}
	}
	{
		draft, err := session.NewEventDraft(session.RequestHeaderSet,
			session.RequestHeaderSnapshot{
				Header: headerValue,
				Reason: session.RequestHeaderInitial,
			})
		if err == nil {
			_, err = conversation.Commit(context.Background(), session.Batch(draft))
		}
		if err != nil {
			testingContext.Fatal(err)
		}
	}
	appendAssistantWithSources(
		testingContext,
		conversation,
		durableText,
		usageValue,
		sourceSeqs,
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
			testingContext.Fatal(err)
		}
	}
}

func appendAssistantWithSources(
	testingContext *testing.T,
	conversation session.Context,
	textValue string,
	usageValue *llm.TokenUsage,
	sourceSeqs *[]int64,
) int64 {
	testingContext.Helper()
	assistantValue := newAssistantMessage(testingContext, textValue)
	committed, err := session.Event{}, error(nil)
	{
		var committedEvent session.Event
		var writeErr error
		draft, draftErr := session.NewSurfaceEventDraft(session.AssistantMessaged,
			session.AssistantMessage{
				Turn:    1,
				Step:    1,
				Message: assistantValue,
				Usage:   usageValue,
			},
			session.SurfaceIntent{
				Operation:       session.SurfaceAppend(),
				SourceEventSeqs: sourceSeqs,
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
	return committed.Seq
}

func newAssistantMessage(testingContext *testing.T, textValue string) llm.AssistantMessage {
	testingContext.Helper()
	content := []llm.ContentBlock{}
	if textValue != "" {
		content = append(content, llm.NewTextBlock(textValue))
	}
	messageValue, err := llm.NewAssistantMessage(llm.AssistantMessageInput{
		Content: content,
		Source: llm.ModelMessageSource{
			Provider: "mock",
			Model:    "model-a",
		},
	})
	if err != nil {
		testingContext.Fatal(err)
	}
	return messageValue
}

func pointerToSeqs(values []int64) *[]int64 {
	detached := append([]int64(nil), values...)
	return &detached
}

func sumNodeTokens(nodes []SurfaceNode) int64 {
	var total int64
	for _, nodeValue := range nodes {
		total += nodeValue.Tokens
	}
	return total
}
