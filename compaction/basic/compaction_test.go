package basic

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/gorenx/goren/compaction"
	"github.com/gorenx/goren/compaction/toolresultpruner"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/llm/tokenmeter"
	"github.com/gorenx/goren/session"
)

func TestCompactIfNeededSkipsWithoutDurableRoute(t *testing.T) {
	t.Parallel()
	measureCalls := 0
	meterValue := &meterStub{
		measure: func(
			context.Context,
			*session.Session,
			*session.EpochHeader,
		) (tokenmeter.Measurement, error) {
			measureCalls++
			return tokenmeter.Measurement{}, nil
		},
	}
	implementation := newBoundCompaction(
		t,
		Config{
			Auto: boolPointer(false),
		},
		newRuntimeStub("checkpoint", 1_000),
		meterValue,
		&liveStoreStub{},
		nil,
	)
	conversation, err := session.New("headerless", session.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := implementation.CompactIfNeeded(
		context.Background(),
		compaction.AgentContext{
			Session:  conversation,
			Provider: fixtureProvider,
			Model:    fixtureModel,
		},
		compaction.TriggerPressure,
	)
	if err != nil || outcome != nil || measureCalls != 0 {
		t.Fatalf("outcome = %#v, error = %v, measure calls = %d", outcome, err, measureCalls)
	}
}

func TestPressurePrunesOnlyAfterThresholdAndRemeasures(t *testing.T) {
	t.Parallel()
	conversation := conversationFixture(t, 3, "pressure")
	pruned := false
	measureCalls := 0
	meterValue := &meterStub{
		measure: func(
			_ context.Context,
			current *session.Session,
			_ *session.EpochHeader,
		) (tokenmeter.Measurement, error) {
			measureCalls++
			measurement := pricedSurface(current, 100, 0)
			if pruned {
				measurement.TotalTokens = 300
			}
			return measurement, nil
		},
	}
	prunerValue := &prunerStub{
		prune: func(
			context.Context,
			*session.Session,
		) (toolresultpruner.Result, error) {
			pruned = true
			return toolresultpruner.Result{}, nil
		},
	}
	implementation := newBoundCompaction(
		t,
		Config{
			PolicyConfig: PolicyConfig{
				ThresholdRatio: floatPointer(0.5),
				RetainTokens:   int64Pointer(100),
			},
			Auto: boolPointer(false),
		},
		newRuntimeStub("checkpoint", 1_000),
		meterValue,
		&liveStoreStub{},
		prunerValue,
	)
	outcome, err := implementation.CompactIfNeeded(
		context.Background(),
		compaction.AgentContext{
			Session: conversation,
		},
		compaction.TriggerPressure,
	)
	if err != nil || outcome != nil || prunerValue.calls != 1 || measureCalls != 2 {
		t.Fatalf(
			"outcome = %#v, error = %v, prune calls = %d, measure calls = %d",
			outcome,
			err,
			prunerValue.calls,
			measureCalls,
		)
	}
	if conversation.Surface().ReplaceGeneration != 0 {
		t.Fatalf("pruner fixture unexpectedly changed Surface = %#v", conversation.Surface())
	}
}

func TestPressureCompactsHeadAndConvergesBelowThreshold(t *testing.T) {
	t.Parallel()
	conversation := conversationFixture(t, 3, "pressure")
	implementation := newBoundCompaction(
		t,
		Config{
			PolicyConfig: PolicyConfig{
				ThresholdRatio: floatPointer(0.5),
				RetainTokens:   int64Pointer(100),
			},
			Auto: boolPointer(false),
		},
		newRuntimeStub("small checkpoint", 1_000),
		&meterStub{},
		&liveStoreStub{},
		nil,
	)
	before := conversation.Surface()
	outcome, err := implementation.CompactIfNeeded(
		context.Background(),
		compaction.AgentContext{
			Session: conversation,
		},
		compaction.TriggerPressure,
	)
	if err != nil {
		t.Fatal(err)
	}
	if outcome == nil || len(outcome.ShadowedSeqs) != len(before.Nodes)-1 {
		t.Fatalf("pressure outcome = %#v, before = %#v", outcome, before)
	}
	after := conversation.Surface()
	if len(after.Nodes) != 2 || after.ReplaceGeneration != 1 {
		t.Fatalf("pressure Surface = %#v", after)
	}
}

func TestPressureRequiresCapacityButOverflowDoesNot(t *testing.T) {
	t.Parallel()
	missingContext := newRuntimeStub("checkpoint", 1_000)
	missingContext.resolved.Context = nil
	implementation := newBoundCompaction(
		t,
		Config{
			PolicyConfig: PolicyConfig{
				ThresholdRatio: floatPointer(0.5),
				RetainTokens:   int64Pointer(100),
			},
			Auto: boolPointer(false),
		},
		missingContext,
		&meterStub{},
		&liveStoreStub{},
		nil,
	)
	pressureConversation := conversationFixture(t, 3, "pressure")
	_, err := implementation.CompactIfNeeded(
		context.Background(),
		compaction.AgentContext{
			Session: pressureConversation,
		},
		compaction.TriggerPressure,
	)
	var targetProblem *TargetPressureConfigError
	if !errors.As(err, &targetProblem) ||
		targetProblem.TargetKey != fixtureProvider+"/"+fixtureModel {
		t.Fatalf("pressure error = %T %v", err, err)
	}

	overflowConversation := conversationFixture(t, 3, "overflow")
	outcome, err := implementation.CompactIfNeeded(
		context.Background(),
		compaction.AgentContext{
			Session: overflowConversation,
		},
		compaction.TriggerContextOverflow,
	)
	if err != nil || outcome == nil {
		t.Fatalf("overflow outcome = %#v, error = %v", outcome, err)
	}
}

func TestOverflowPrunesBeforeForcedCompaction(t *testing.T) {
	t.Parallel()
	conversation := conversationFixture(t, 3, "overflow")
	pruneCalls := 0
	prunerValue := &prunerStub{
		prune: func(
			context.Context,
			*session.Session,
		) (toolresultpruner.Result, error) {
			pruneCalls++
			return toolresultpruner.Result{}, nil
		},
	}
	implementation := newBoundCompaction(
		t,
		Config{
			PolicyConfig: PolicyConfig{
				ThresholdRatio: floatPointer(1),
				RetainTokens:   int64Pointer(900),
			},
			Auto: boolPointer(false),
		},
		newRuntimeStub("checkpoint", 100_000),
		&meterStub{},
		&liveStoreStub{},
		prunerValue,
	)
	outcome, err := implementation.CompactIfNeeded(
		context.Background(),
		compaction.AgentContext{
			Session: conversation,
		},
		compaction.TriggerContextOverflow,
	)
	if err != nil || outcome == nil || pruneCalls != 1 {
		t.Fatalf("overflow outcome = %#v, error = %v, prune calls = %d", outcome, err, pruneCalls)
	}
}

func TestPressureDoesNotPruneBelowThreshold(t *testing.T) {
	t.Parallel()
	conversation := conversationFixture(t, 1, "small")
	prunerValue := &prunerStub{}
	implementation := newBoundCompaction(
		t,
		Config{
			PolicyConfig: PolicyConfig{
				ThresholdRatio: floatPointer(0.8),
				RetainTokens:   int64Pointer(100),
			},
			Auto: boolPointer(false),
		},
		newRuntimeStub("checkpoint", 1_000),
		&meterStub{},
		&liveStoreStub{},
		prunerValue,
	)
	outcome, err := implementation.CompactIfNeeded(
		context.Background(),
		compaction.AgentContext{
			Session: conversation,
		},
		compaction.TriggerPressure,
	)
	if err != nil || outcome != nil || prunerValue.calls != 0 {
		t.Fatalf("outcome = %#v, error = %v, prune calls = %d", outcome, err, prunerValue.calls)
	}
}

func TestPressureReportsFailureAfterBoundedAttempts(t *testing.T) {
	t.Parallel()
	conversation := conversationFixture(t, 3, "pressure")
	meterValue := &meterStub{
		measure: func(
			_ context.Context,
			current *session.Session,
			_ *session.EpochHeader,
		) (tokenmeter.Measurement, error) {
			measurement := pricedSurface(current, 100, 0)
			measurement.TotalTokens = 900
			return measurement, nil
		},
	}
	implementation := newBoundCompaction(
		t,
		Config{
			PolicyConfig: PolicyConfig{
				ThresholdRatio:    floatPointer(0.5),
				RetainTokens:      int64Pointer(100),
				CompactionRetries: intPointer(0),
			},
			Auto: boolPointer(false),
		},
		newRuntimeStub("checkpoint", 1_000),
		meterValue,
		&liveStoreStub{},
		nil,
	)
	_, err := implementation.CompactIfNeeded(
		context.Background(),
		compaction.AgentContext{
			Session: conversation,
		},
		compaction.TriggerPressure,
	)
	if err == nil || !strings.Contains(err.Error(), "above threshold after 1 compaction attempts") {
		t.Fatalf("pressure error = %v", err)
	}
}

func TestPrunerFailureDoesNotStartCompaction(t *testing.T) {
	t.Parallel()
	conversation := conversationFixture(t, 3, "pressure")
	want := errors.New("pruner failed")
	prunerValue := &prunerStub{
		prune: func(
			context.Context,
			*session.Session,
		) (toolresultpruner.Result, error) {
			return toolresultpruner.Result{}, want
		},
	}
	implementation := newBoundCompaction(
		t,
		Config{
			PolicyConfig: PolicyConfig{
				ThresholdRatio: floatPointer(0.5),
				RetainTokens:   int64Pointer(100),
			},
			Auto: boolPointer(false),
		},
		newRuntimeStub("checkpoint", 1_000),
		&meterStub{},
		&liveStoreStub{},
		prunerValue,
	)
	_, err := implementation.CompactIfNeeded(
		context.Background(),
		compaction.AgentContext{
			Session: conversation,
		},
		compaction.TriggerPressure,
	)
	if !errors.Is(err, want) {
		t.Fatalf("pruner error = %v", err)
	}
	for _, entry := range conversation.Events() {
		if entry.Type == compaction.StartEventName {
			t.Fatalf("compaction started after pruner failure: %#v", entry)
		}
	}
}

func TestPressureRechecksCompactionLockAfterResolvingCapacity(t *testing.T) {
	t.Parallel()
	conversation := conversationFixture(t, 2, strings.Repeat("pressure ", 20))
	runtimeValue := newRuntimeStub("unused", 1_000)
	runtimeValue.beforeResolve = func() {
		if _, err := session.AppendSerialized(
			conversation,
			compaction.StartEvent,
			compaction.Start{
				CompactionID: "concurrent-compaction",
				Turn:         int64Pointer(3),
			},
		); err != nil {
			panic(err)
		}
	}
	prunerValue := &prunerStub{}
	implementation := newBoundCompaction(
		t,
		Config{
			PolicyConfig: PolicyConfig{
				ThresholdRatio: floatPointer(0.1),
				RetainTokens:   int64Pointer(0),
			},
		},
		runtimeValue,
		&meterStub{},
		&liveStoreStub{},
		prunerValue,
	)
	_, err := implementation.CompactIfNeeded(
		context.Background(),
		compaction.AgentContext{
			Session: conversation,
		},
		compaction.TriggerPressure,
	)
	assertManualErrorCode(t, err, compaction.ManualErrorBusy)
	if prunerValue.calls != 0 || len(runtimeValue.requestValues()) != 0 {
		t.Fatalf(
			"work after concurrent lock = pruner %d, summary requests %d",
			prunerValue.calls,
			len(runtimeValue.requestValues()),
		)
	}
}

var _ llm.LlmRuntime = (*runtimeStub)(nil)
