package compaction_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
	"unicode/utf8"

	"github.com/gorenx/goren/compaction"
	"github.com/gorenx/goren/compaction/basic"
	"github.com/gorenx/goren/compaction/toolresultpruner"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/session"
)

const compactionSourceBaseline = "b150a551b8d465e31e418e1b2eaf5e79bbb7d28e"

type compactionFixtureDocument struct {
	SchemaVersion int `json:"schemaVersion"`
	Source        struct {
		Commit  string `json:"commit"`
		Version string `json:"version"`
	} `json:"source"`
	BasicConfig []struct {
		Name   string          `json:"name"`
		Input  json.RawMessage `json:"input"`
		Target struct {
			Provider string `json:"provider"`
			Model    string `json:"model"`
		} `json:"target"`
		ContextWindow int             `json:"contextWindow"`
		Resolved      json.RawMessage `json:"resolved"`
		CompactSpec   json.RawMessage `json:"compactSpec"`
	} `json:"basicConfig"`
	PrunerConfig []struct {
		Name     string          `json:"name"`
		Input    json.RawMessage `json:"input"`
		Resolved json.RawMessage `json:"resolved"`
	} `json:"prunerConfig"`
	PruneMarker struct {
		Value      string `json:"value"`
		CodePoints int    `json:"codePoints"`
	} `json:"pruneMarker"`
	CheckpointSource json.RawMessage `json:"checkpointSource"`
	Events           []session.Event `json:"events"`
	Result           struct {
		CompactionID       compaction.ID           `json:"compactionId"`
		SourceCommandID    *string                 `json:"sourceCommandId"`
		StartSeq           int64                   `json:"startSeq"`
		SummarySeq         int64                   `json:"summarySeq"`
		EndSeq             int64                   `json:"endSeq"`
		Summary            json.RawMessage         `json:"summary"`
		ShadowedRange      compaction.SurfaceRange `json:"shadowedRange"`
		ShadowedSeqs       []int64                 `json:"shadowedSeqs"`
		ShadowedTokenCount int64                   `json:"shadowedTokenCount"`
	} `json:"result"`
}

type basicResolvedWire struct {
	ThresholdRatio        float64                   `json:"thresholdRatio"`
	RetainRatio           *float64                  `json:"retainRatio,omitempty"`
	RetainTokens          *int64                    `json:"retainTokens,omitempty"`
	SummarizationProvider string                    `json:"summarizationProvider"`
	SummarizationModel    string                    `json:"summarizationModel"`
	MaxTokens             int                       `json:"maxTokens"`
	CompactionRetries     int                       `json:"compactionRetries"`
	MaxOverflowRetries    int                       `json:"maxOverflowRetries"`
	ModelPolicies         []basic.ModelPolicyConfig `json:"modelPolicies"`
	Auto                  bool                      `json:"auto"`
}

type basicCompactSpecWire struct {
	Target struct {
		Provider string `json:"provider"`
		Model    string `json:"model"`
	} `json:"target"`
	ContextWindow         int     `json:"contextWindow"`
	ThresholdRatio        float64 `json:"thresholdRatio"`
	ThresholdTokens       int64   `json:"thresholdTokens"`
	RetainTokens          int64   `json:"retainTokens"`
	SummarizationProvider string  `json:"summarizationProvider"`
	SummarizationModel    string  `json:"summarizationModel"`
	MaxTokens             int     `json:"maxTokens"`
	CompactionRetries     int     `json:"compactionRetries"`
	MaxOverflowRetries    int     `json:"maxOverflowRetries"`
}

func TestPinnedSourceCompactionVectorsMatchGoContracts(t *testing.T) {
	documentValue := loadCompactionFixture(t)
	if documentValue.SchemaVersion != 1 ||
		documentValue.Source.Commit != compactionSourceBaseline ||
		documentValue.Source.Version != "0.1.1-rc.2" {
		t.Fatalf("Compaction fixture provenance = %#v", documentValue.Source)
	}

	for _, testCase := range documentValue.BasicConfig {
		t.Run("basic-config/"+testCase.Name, func(t *testing.T) {
			var settings basic.Config
			if err := json.Unmarshal(testCase.Input, &settings); err != nil {
				t.Fatal(err)
			}
			resolvedPolicy, err := basic.ResolveConfig(settings)
			if err != nil {
				t.Fatal(err)
			}
			assertEquivalentJSON(t, testCase.Resolved, basicResolvedWire{
				ThresholdRatio:        resolvedPolicy.ThresholdRatio,
				RetainRatio:           resolvedPolicy.Retention.Ratio,
				RetainTokens:          resolvedPolicy.Retention.Tokens,
				SummarizationProvider: resolvedPolicy.SummarizationProvider,
				SummarizationModel:    resolvedPolicy.SummarizationModel,
				MaxTokens:             resolvedPolicy.MaxTokens,
				CompactionRetries:     resolvedPolicy.CompactionRetries,
				MaxOverflowRetries:    resolvedPolicy.MaxOverflowRetries,
				ModelPolicies:         resolvedPolicy.ModelPolicies,
				Auto:                  resolvedPolicy.Auto,
			})
			routedPolicy := basic.ResolveTargetPolicy(
				resolvedPolicy,
				basic.RouteTarget{
					Provider: testCase.Target.Provider,
					Model:    testCase.Target.Model,
				},
			)
			selectedSpec, err := basic.ResolveCompactSpec(
				routedPolicy,
				testCase.ContextWindow,
			)
			if err != nil {
				t.Fatal(err)
			}
			wireValue := basicCompactSpecWire{
				ContextWindow:         selectedSpec.ContextWindow,
				ThresholdRatio:        selectedSpec.ThresholdRatio,
				ThresholdTokens:       selectedSpec.ThresholdTokens,
				RetainTokens:          selectedSpec.RetainTokens,
				SummarizationProvider: selectedSpec.SummarizationProvider,
				SummarizationModel:    selectedSpec.SummarizationModel,
				MaxTokens:             selectedSpec.MaxTokens,
				CompactionRetries:     selectedSpec.CompactionRetries,
				MaxOverflowRetries:    selectedSpec.MaxOverflowRetries,
			}
			wireValue.Target.Provider = selectedSpec.Target.Provider
			wireValue.Target.Model = selectedSpec.Target.Model
			assertEquivalentJSON(t, testCase.CompactSpec, wireValue)
		})
	}

	for _, testCase := range documentValue.PrunerConfig {
		t.Run("pruner-config/"+testCase.Name, func(t *testing.T) {
			var settings toolresultpruner.Config
			if err := json.Unmarshal(testCase.Input, &settings); err != nil {
				t.Fatal(err)
			}
			resolvedBudget, err := toolresultpruner.ResolveConfig(settings)
			if err != nil {
				t.Fatal(err)
			}
			assertEquivalentJSON(t, testCase.Resolved, struct {
				ThresholdChars int `json:"thresholdChars"`
				HeadChars      int `json:"headChars"`
				TailChars      int `json:"tailChars"`
			}{
				ThresholdChars: resolvedBudget.ThresholdChars,
				HeadChars:      resolvedBudget.HeadChars,
				TailChars:      resolvedBudget.TailChars,
			})
		})
	}
	if documentValue.PruneMarker.Value != toolresultpruner.PruneMarker ||
		documentValue.PruneMarker.CodePoints != utf8.RuneCountInString(
			toolresultpruner.PruneMarker,
		) {
		t.Fatalf("prune marker fixture = %#v", documentValue.PruneMarker)
	}

	assertCompactionEventVectors(t, documentValue)
}

func assertCompactionEventVectors(
	testingContext *testing.T,
	documentValue compactionFixtureDocument,
) {
	testingContext.Helper()
	var startValue compaction.Start
	var summaryValue compaction.Summary
	var endValue compaction.End
	var checkpointMessage llm.UserMessage
	for _, entry := range documentValue.Events {
		if err := compaction.ValidateEvent(entry); err != nil {
			testingContext.Fatal(err)
		}
		switch entry.Type {
		case compaction.StartEventName:
			startValue, _ = compaction.DecodeStart(entry.Data)
		case compaction.SummaryEventName:
			summaryValue, _ = compaction.DecodeSummary(entry.Data)
		case session.UserMessageEventName:
			messageValue, err := session.DeriveEventMessage(entry)
			if err != nil {
				testingContext.Fatal(err)
			}
			typedMessage, valid := messageValue.(llm.UserMessage)
			if !valid || !compaction.IsCheckpointSource(typedMessage.SourceValue()) {
				testingContext.Fatalf("checkpoint fixture message = %#v", messageValue)
			}
			checkpointMessage = typedMessage
		case compaction.EndEventName:
			endValue, _ = compaction.DecodeEnd(entry.Data)
		}
	}
	state, err := compaction.InspectLog(documentValue.Events)
	if err != nil || state.Attempt != nil {
		testingContext.Fatalf("source fixture log state = %#v, error = %v", state, err)
	}

	assertEquivalentJSON(
		testingContext,
		documentValue.CheckpointSource,
		checkpointMessage.SourceValue(),
	)
	checkpointOrigin, err := compaction.NewCheckpointSource(
		documentValue.Result.CompactionID,
		documentValue.Result.SourceCommandID,
	)
	if err != nil {
		testingContext.Fatal(err)
	}
	assertEquivalentJSON(
		testingContext,
		documentValue.CheckpointSource,
		checkpointOrigin,
	)
	resultBlocks, err := llm.DecodeContentBlocks(documentValue.Result.Summary)
	if err != nil {
		testingContext.Fatal(err)
	}
	if documentValue.Result.CompactionID != startValue.CompactionID ||
		documentValue.Result.CompactionID != summaryValue.CompactionID ||
		documentValue.Result.CompactionID != endValue.CompactionID ||
		documentValue.Result.StartSeq != 1 ||
		documentValue.Result.SummarySeq != 2 ||
		documentValue.Result.EndSeq != 4 ||
		!reflect.DeepEqual(documentValue.Result.SourceCommandID, startValue.SourceCommandID) ||
		!reflect.DeepEqual(resultBlocks, summaryValue.Summary) ||
		documentValue.Result.ShadowedRange != summaryValue.ShadowedRange ||
		!reflect.DeepEqual(documentValue.Result.ShadowedSeqs, summaryValue.ShadowedSeqs) ||
		documentValue.Result.ShadowedTokenCount != summaryValue.ShadowedTokenCount {
		testingContext.Fatalf("Compaction result fixture does not match decoded events")
	}
}

func loadCompactionFixture(testingContext *testing.T) compactionFixtureDocument {
	testingContext.Helper()
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		testingContext.Fatal("resolve Compaction fixture path")
	}
	fixturePath := filepath.Join(
		filepath.Dir(currentFile),
		"..",
		"contracts",
		"deepseek-harness",
		"compaction-vectors.json",
	)
	content, err := os.ReadFile(fixturePath)
	if err != nil {
		testingContext.Fatal(err)
	}
	var documentValue compactionFixtureDocument
	if err = json.Unmarshal(content, &documentValue); err != nil {
		testingContext.Fatal(err)
	}
	return documentValue
}

func assertEquivalentJSON(
	testingContext *testing.T,
	want json.RawMessage,
	actual any,
) {
	testingContext.Helper()
	encoded, err := json.Marshal(actual)
	if err != nil {
		testingContext.Fatal(err)
	}
	var wantValue any
	if err = json.Unmarshal(want, &wantValue); err != nil {
		testingContext.Fatal(err)
	}
	var actualValue any
	if err = json.Unmarshal(encoded, &actualValue); err != nil {
		testingContext.Fatal(err)
	}
	if !reflect.DeepEqual(actualValue, wantValue) {
		testingContext.Fatalf(
			"JSON mismatch\nactual: %s\nwant: %s",
			encoded,
			want,
		)
	}
}
