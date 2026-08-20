package contract_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"testing"

	agentcore "github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/agentloop"
	"github.com/gorenx/goren/apiproxy"
	"github.com/gorenx/goren/connection"
	"github.com/gorenx/goren/internal/llm/deepseek"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/llmretry"
	"github.com/gorenx/goren/session"
	sessionprojection "github.com/gorenx/goren/session/projection"
	sessiontitle "github.com/gorenx/goren/session/title"
	"github.com/gorenx/goren/systemprompt"
	toolscore "github.com/gorenx/goren/tools"
)

type contractManifest struct {
	SchemaVersion int `json:"schemaVersion"`
	Source        struct {
		Commit  string `json:"commit"`
		Version string `json:"version"`
	} `json:"source"`
	Included struct {
		HTTP struct {
			APIPath        string `json:"apiPath"`
			RespondPath    string `json:"respondPath"`
			MuxEventsPath  string `json:"muxEventsPath"`
			HostEventsPath string `json:"hostEventsPath"`
		} `json:"http"`
		MessageTypes   []string `json:"messageTypes"`
		ReceiptReasons []string `json:"receiptReasons"`
		UnaryMethods   []string `json:"unaryMethods"`
		MuxFrameTypes  []string `json:"muxFrameTypes"`
		HostFrameTypes []string `json:"hostFrameTypes"`
		Agent          struct {
			Service      string   `json:"service"`
			Events       []string `json:"events"`
			InboxTargets []string `json:"inboxTargets"`
			Statuses     []string `json:"statuses"`
		} `json:"agent"`
		AgentLoop struct {
			Service                     string   `json:"service"`
			DefaultMaxParallelToolCalls int      `json:"defaultMaxParallelToolCalls"`
			SessionEvents               []string `json:"sessionEvents"`
		} `json:"agentLoop"`
		SystemPrompt struct {
			Service         string   `json:"service"`
			Events          []string `json:"events"`
			BuiltinSections []string `json:"builtinSections"`
			ToolOrderRest   string   `json:"toolOrderRest"`
		} `json:"systemPrompt"`
		Tools struct {
			Service           string   `json:"service"`
			Events            []string `json:"events"`
			ErrorCodes        []string `json:"errorCodes"`
			ReservedToolName  string   `json:"reservedToolName"`
			PresentationModes []string `json:"presentationModes"`
		} `json:"tools"`
		LLM struct {
			Service               string   `json:"service"`
			Events                []string `json:"events"`
			ProviderRoute         string   `json:"providerRoute"`
			DefaultModels         []string `json:"defaultModels"`
			ContentTypes          []string `json:"contentTypes"`
			StreamChunkTypes      []string `json:"streamChunkTypes"`
			DefaultRetryableCodes []string `json:"defaultRetryableCodes"`
		} `json:"llm"`
		LLMRetry struct {
			Factory       string   `json:"factory"`
			SessionEvents []string `json:"sessionEvents"`
		} `json:"llmRetry"`
		SessionProjection struct {
			Service   string `json:"service"`
			FrameType string `json:"frameType"`
		} `json:"sessionProjection"`
		SessionTitle struct {
			Service       string `json:"service"`
			SessionEvent  string `json:"sessionEvent"`
			ProjectionKey string `json:"projectionKey"`
		} `json:"sessionTitle"`
	} `json:"included"`
}

type fixtureDocument struct {
	SchemaVersion int `json:"schemaVersion"`
	Source        struct {
		Commit  string `json:"commit"`
		Version string `json:"version"`
	} `json:"source"`
	Suites map[string][]contractVector `json:"suites"`
}

type contractVector struct {
	Name       string          `json:"name"`
	Accepted   bool            `json:"accepted"`
	Input      json.RawMessage `json:"input"`
	Normalized json.RawMessage `json:"normalized"`
}

func TestPinnedManifestMatchesGoSurface(t *testing.T) {
	t.Parallel()
	manifestDocument := loadManifest(t)
	if manifestDocument.SchemaVersion != 1 {
		t.Fatalf("schemaVersion = %d", manifestDocument.SchemaVersion)
	}
	httpSurface := manifestDocument.Included.HTTP
	if httpSurface.APIPath != connection.APIPath || httpSurface.RespondPath != connection.RespondPath ||
		httpSurface.MuxEventsPath != connection.MuxEventsPath || httpSurface.HostEventsPath != connection.HostEventsPath {
		t.Fatalf("HTTP surface = %#v", httpSurface)
	}
	if !slices.Equal(manifestDocument.Included.MessageTypes, []string{
		connection.ClientRequestType, connection.ServerResponseType,
		connection.ServerRequestType, connection.ClientResponseType,
	}) {
		t.Fatalf("message types = %v", manifestDocument.Included.MessageTypes)
	}
	if !slices.Equal(manifestDocument.Included.ReceiptReasons, []string{
		string(connection.ReceiptNotPending), string(connection.ReceiptBadResponse),
	}) {
		t.Fatalf("receipt reasons = %v", manifestDocument.Included.ReceiptReasons)
	}
	if !slices.Equal(manifestDocument.Included.UnaryMethods, []string{
		apiproxy.HostDescribeMethod,
		apiproxy.AgentPresetListMethod,
		apiproxy.SettingsDescribeMethod,
		apiproxy.CredentialsDescribeMethod,
		apiproxy.CredentialsSetMethod,
		apiproxy.CredentialsUnsetMethod,
		apiproxy.LLMProvidersMethod,
		apiproxy.LLMModelsMethod,
		apiproxy.SessionListMethod,
		apiproxy.SessionSearchMethod,
		apiproxy.SessionCreateMethod,
		apiproxy.SessionRenameMethod,
		apiproxy.SessionHistoryMethod,
		apiproxy.SessionModelsMethod,
		apiproxy.SessionSelectModelMethod,
		apiproxy.SessionPromptMethod,
		apiproxy.SessionUpdateQueueMethod,
		apiproxy.SessionCancelMethod,
		apiproxy.WorkspaceListMethod,
		apiproxy.WorkspaceCreateMethod,
		apiproxy.WorkspaceRenameMethod,
		apiproxy.WorkspaceDeleteMethod,
		apiproxy.WorkspaceInsertBeforeMethod,
		apiproxy.WorkspaceInsertSessionBeforeMethod,
		apiproxy.WorkspaceArchiveSessionMethod,
	}) {
		t.Fatalf("unary methods = %v", manifestDocument.Included.UnaryMethods)
	}
	agentSurface := manifestDocument.Included.Agent
	if agentSurface.Service != agentcore.ServiceName ||
		!slices.Equal(agentSurface.Events, []string{
			agentcore.CreatedEventName, agentcore.DisposedEventName, agentcore.StatusEventName,
			agentcore.InboxInsertedEventName, agentcore.InboxClaimedEventName, agentcore.InboxDiscardedEventName,
			agentcore.SessionStartEventName, agentcore.PreStepEventName, agentcore.RequestEventName,
			agentcore.RequestErrorEventName, agentcore.TurnStoppingEventName, agentcore.ErrorEventName,
		}) || !slices.Equal(agentSurface.InboxTargets, []string{string(agentcore.NextTurn), string(agentcore.NextStep)}) ||
		!slices.Equal(agentSurface.Statuses, []string{string(agentcore.StatusIdle), string(agentcore.StatusRunning)}) {
		t.Fatalf("agent surface = %#v", agentSurface)
	}
	loopSurface := manifestDocument.Included.AgentLoop
	if loopSurface.Service != agentloop.ServiceName ||
		loopSurface.DefaultMaxParallelToolCalls != agentloop.DefaultMaxParallelToolCalls ||
		!slices.Equal(loopSurface.SessionEvents, []string{
			session.TurnStartEventName, session.TurnEndEventName,
			session.StepStartEventName, session.StepEndEventName,
			session.UserMessageEventName, session.AssistantChunkEventName,
			session.AssistantMessageEventName, session.ToolCallEventName,
			session.ToolResultEventName, session.RequestHeaderEventName,
			session.RequestContextEventName,
		}) {
		t.Fatalf("agent loop surface = %#v", loopSurface)
	}
	promptSurface := manifestDocument.Included.SystemPrompt
	if promptSurface.Service != systemprompt.ServiceName ||
		!slices.Equal(promptSurface.Events, []string{systemprompt.AssembleEventName, systemprompt.ChangeEventName}) ||
		!slices.Equal(promptSurface.BuiltinSections, []string{"harness:identity", systemprompt.PersonaSection}) ||
		promptSurface.ToolOrderRest != systemprompt.ToolOrderRest {
		t.Fatalf("system prompt surface = %#v", promptSurface)
	}
	toolSurface := manifestDocument.Included.Tools
	if toolSurface.Service != toolscore.ServiceName ||
		!slices.Equal(toolSurface.Events, []string{
			toolscore.PreExecuteEventName, toolscore.ExecuteEventName, toolscore.PostExecuteEventName,
			toolscore.ResultEventName, toolscore.ChangeEventName,
		}) ||
		!slices.Equal(toolSurface.ErrorCodes, []string{
			"UNKNOWN_TOOL", "INVALID_ARGS", "INVALID_TOOL_OUTPUT",
			toolscore.ToolAborted, toolscore.ToolAbortedBeforeDispatch,
		}) || toolSurface.ReservedToolName != toolscore.RunCodeName ||
		!slices.Equal(toolSurface.PresentationModes, []string{string(toolscore.PresentationNative)}) {
		t.Fatalf("tools surface = %#v", toolSurface)
	}
	llmSurface := manifestDocument.Included.LLM
	if llmSurface.Service != llm.ServiceName ||
		!slices.Equal(llmSurface.Events, []string{llm.AdaptersUpdatedEventName, llm.StreamEventName}) ||
		llmSurface.ProviderRoute != deepseek.ProviderRoute ||
		!slices.Equal(llmSurface.DefaultModels, []string{"deepseek-v4-flash", "deepseek-v4-pro"}) ||
		!slices.Equal(llmSurface.ContentTypes, []string{"text", "reasoning", "image", "tool-call", "tool-result"}) ||
		!slices.Equal(llmSurface.StreamChunkTypes, []string{
			"block-start", "text-delta", "reasoning-delta", "tool-call-delta", "block-end", "usage", "finish",
		}) || !slices.Equal(llmSurface.DefaultRetryableCodes, []string{
		llm.EmptyResponseCode, "RATE_LIMIT", "SERVER", "TIMEOUT", "TRANSPORT",
	}) {
		t.Fatalf("llm surface = %#v", llmSurface)
	}
	retrySurface := manifestDocument.Included.LLMRetry
	if retrySurface.Factory != "@deepseek-ai/dsh-llm-retry" ||
		!slices.Equal(retrySurface.SessionEvents, []string{llmretry.RetryEventName, llmretry.RetryStartedEventName}) {
		t.Fatalf("llm retry surface = %#v", retrySurface)
	}
	projectionSurface := manifestDocument.Included.SessionProjection
	if projectionSurface.Service != sessionprojection.ServiceName || projectionSurface.FrameType != "session/projection" {
		t.Fatalf("session projection surface = %#v", projectionSurface)
	}
	titleSurface := manifestDocument.Included.SessionTitle
	if titleSurface.Service != sessiontitle.ServiceName || titleSurface.SessionEvent != sessiontitle.TitleEventName ||
		titleSurface.ProjectionKey != sessiontitle.ProjectionKey {
		t.Fatalf("session title surface = %#v", titleSurface)
	}

	muxNames := encodedMuxFrames(t)
	if !slices.Equal(manifestDocument.Included.MuxFrameTypes, muxNames) {
		t.Fatalf("mux frame types = %v, encoded = %v", manifestDocument.Included.MuxFrameTypes, muxNames)
	}
	hostNames := encodedHostFrames(t)
	if !slices.Equal(manifestDocument.Included.HostFrameTypes, hostNames) {
		t.Fatalf("host frame types = %v, encoded = %v", manifestDocument.Included.HostFrameTypes, hostNames)
	}
}

func TestGoAgreesWithPinnedSourceVectors(t *testing.T) {
	t.Parallel()
	manifestDocument := loadManifest(t)
	fixtureData := loadFixtures(t)
	if fixtureData.SchemaVersion != manifestDocument.SchemaVersion ||
		fixtureData.Source.Commit != manifestDocument.Source.Commit ||
		fixtureData.Source.Version != manifestDocument.Source.Version {
		t.Fatalf("fixture provenance = %#v, manifest source = %#v", fixtureData.Source, manifestDocument.Source)
	}

	t.Run("client request", func(t *testing.T) {
		for _, candidate := range fixtureData.Suites["clientRequest"] {
			message, issues := connection.DecodeClientRequest(candidate.Input)
			assertAcceptance(t, candidate, len(issues) == 0)
			if candidate.Accepted {
				assertJSONEqual(t, mustMarshal(t, message), candidate.Normalized)
			}
		}
	})
	t.Run("client response", func(t *testing.T) {
		for _, candidate := range fixtureData.Suites["clientResponse"] {
			message, issues := connection.DecodeClientResponse(candidate.Input)
			assertAcceptance(t, candidate, len(issues) == 0)
			if candidate.Accepted {
				assertJSONEqual(t, mustMarshal(t, message), candidate.Normalized)
			}
		}
	})
	t.Run("host describe request", func(t *testing.T) {
		for _, candidate := range fixtureData.Suites["hostDescribeRequest"] {
			payload, issues := apiproxy.DecodeObject[apiproxy.HostDescribeRequest](candidate.Input)
			assertAcceptance(t, candidate, len(issues) == 0)
			if candidate.Accepted {
				assertJSONEqual(t, mustMarshal(t, payload), candidate.Normalized)
			}
		}
	})
	t.Run("host describe value", func(t *testing.T) {
		for _, candidate := range fixtureData.Suites["hostDescribeValue"] {
			var snapshot apiproxy.HostDescription
			decodeErr := json.Unmarshal(candidate.Input, &snapshot)
			accepted := false
			if decodeErr == nil {
				methods := apiproxy.NewCatalog()
				provider := apiproxy.HostDescriptionFunc(func(context.Context) (apiproxy.HostDescription, error) {
					return snapshot, nil
				})
				if err := apiproxy.RegisterHostDescribe(methods, provider); err != nil {
					t.Fatal(err)
				}
				outcome, dispatchErr := methods.DispatchUnary(
					context.Background(), apiproxy.HostDescribeMethod, "fixture-rpc", json.RawMessage(`{}`),
				)
				accepted = dispatchErr == nil && outcome.OK
			}
			assertAcceptance(t, candidate, accepted)
			if candidate.Accepted {
				assertJSONEqual(t, mustMarshal(t, snapshot), candidate.Normalized)
			}
		}
	})
	t.Run("session requests", func(t *testing.T) {
		decoders := map[string]func(json.RawMessage) []connection.ValidationIssue{
			"sessionListRequest": func(rawValue json.RawMessage) []connection.ValidationIssue {
				_, issues := apiproxy.DecodeSessionListRequest(rawValue)
				return issues
			},
			"sessionSearchRequest": func(rawValue json.RawMessage) []connection.ValidationIssue {
				_, issues := apiproxy.DecodeSessionSearchRequest(rawValue)
				return issues
			},
			"sessionCreateRequest": func(rawValue json.RawMessage) []connection.ValidationIssue {
				_, issues := apiproxy.DecodeSessionCreateRequest(rawValue)
				return issues
			},
			"sessionRenameRequest": func(rawValue json.RawMessage) []connection.ValidationIssue {
				_, issues := apiproxy.DecodeSessionRenameRequest(rawValue)
				return issues
			},
			"sessionHistoryRequest": func(rawValue json.RawMessage) []connection.ValidationIssue {
				_, issues := apiproxy.DecodeSessionHistoryRequest(rawValue)
				return issues
			},
			"sessionModelsRequest": func(rawValue json.RawMessage) []connection.ValidationIssue {
				_, issues := apiproxy.DecodeSessionModelsRequest(rawValue)
				return issues
			},
			"sessionSelectModelRequest": func(rawValue json.RawMessage) []connection.ValidationIssue {
				_, issues := apiproxy.DecodeSessionSelectModelRequest(rawValue)
				return issues
			},
			"sessionPromptRequest": func(rawValue json.RawMessage) []connection.ValidationIssue {
				_, issues := apiproxy.DecodeSessionPromptRequest(rawValue)
				return issues
			},
			"sessionUpdateQueueRequest": func(rawValue json.RawMessage) []connection.ValidationIssue {
				_, issues := apiproxy.DecodeSessionUpdateQueueRequest(rawValue)
				return issues
			},
			"sessionCancelRequest": func(rawValue json.RawMessage) []connection.ValidationIssue {
				_, issues := apiproxy.DecodeSessionCancelRequest(rawValue)
				return issues
			},
		}
		for suiteName, decodePayload := range decoders {
			for _, candidate := range fixtureData.Suites[suiteName] {
				issues := decodePayload(candidate.Input)
				assertAcceptance(t, candidate, len(issues) == 0)
			}
		}
	})
	t.Run("session values", func(t *testing.T) {
		vectors := map[string]map[string]json.RawMessage{
			"sessionListValue":        acceptedByName(t, fixtureData.Suites["sessionListValue"]),
			"sessionSearchValue":      acceptedByName(t, fixtureData.Suites["sessionSearchValue"]),
			"sessionCreateValue":      acceptedByName(t, fixtureData.Suites["sessionCreateValue"]),
			"sessionRenameValue":      acceptedByName(t, fixtureData.Suites["sessionRenameValue"]),
			"sessionHistoryValue":     acceptedByName(t, fixtureData.Suites["sessionHistoryValue"]),
			"sessionModelsValue":      acceptedByName(t, fixtureData.Suites["sessionModelsValue"]),
			"sessionSelectModelValue": acceptedByName(t, fixtureData.Suites["sessionSelectModelValue"]),
			"sessionPromptValue":      acceptedByName(t, fixtureData.Suites["sessionPromptValue"]),
			"sessionUpdateQueueValue": acceptedByName(t, fixtureData.Suites["sessionUpdateQueueValue"]),
			"sessionCancelValue":      acceptedByName(t, fixtureData.Suites["sessionCancelValue"]),
		}
		assertJSONEqual(t, mustMarshal(t, apiproxy.SessionListValue{Items: []apiproxy.SessionSummary{}}), vectors["sessionListValue"]["empty"])
		assertJSONEqual(t, mustMarshal(t, apiproxy.SessionSearchValue{
			Items: []apiproxy.SessionSearchItem{{SessionID: "session-1", Snippet: "matching text"}}, HasMore: true,
		}), vectors["sessionSearchValue"]["match"])
		assertJSONEqual(t, mustMarshal(t, apiproxy.SessionCreateValue{SessionID: "session-1"}), vectors["sessionCreateValue"]["minimal"])
		assertJSONEqual(t, mustMarshal(t, apiproxy.SessionRenameValue{Title: "Named", Seq: 3}), vectors["sessionRenameValue"]["accepted"])
		assertJSONEqual(t, mustMarshal(t, apiproxy.SessionHistoryValue{Events: []apiproxy.HistoryEntry{}, HasMore: false}), vectors["sessionHistoryValue"]["empty"])
		assertJSONEqual(t, mustMarshal(t, apiproxy.SessionModelsValue{
			Current: apiproxy.ModelSelection{Provider: "p", Model: "m"}, Routable: true,
			Groups: []apiproxy.ModelProviderGroup{}, Failures: []apiproxy.ModelCatalogFailure{},
		}), vectors["sessionModelsValue"]["empty-directory"])
		assertJSONEqual(t, mustMarshal(t, apiproxy.SessionSelectModelValue{
			Selected: apiproxy.ModelSelection{Provider: "p", Model: "m"},
		}), vectors["sessionSelectModelValue"]["canonical"])
		accepted := apiproxy.AcceptedValue{Accepted: true}
		assertJSONEqual(t, mustMarshal(t, apiproxy.SessionPromptValue{Accepted: true}), vectors["sessionPromptValue"]["accepted"])
		assertJSONEqual(t, mustMarshal(t, accepted), vectors["sessionUpdateQueueValue"]["accepted"])
		assertJSONEqual(t, mustMarshal(t, accepted), vectors["sessionCancelValue"]["accepted"])
	})
	t.Run("workspace requests", func(t *testing.T) {
		decoders := map[string]func(json.RawMessage) []connection.ValidationIssue{
			"workspaceListRequest": func(rawValue json.RawMessage) []connection.ValidationIssue {
				_, issues := apiproxy.DecodeWorkspaceListRequest(rawValue)
				return issues
			},
			"workspaceCreateRequest": func(rawValue json.RawMessage) []connection.ValidationIssue {
				_, issues := apiproxy.DecodeWorkspaceCreateRequest(rawValue)
				return issues
			},
			"workspaceRenameRequest": func(rawValue json.RawMessage) []connection.ValidationIssue {
				_, issues := apiproxy.DecodeWorkspaceRenameRequest(rawValue)
				return issues
			},
			"workspaceDeleteRequest": func(rawValue json.RawMessage) []connection.ValidationIssue {
				_, issues := apiproxy.DecodeWorkspaceDeleteRequest(rawValue)
				return issues
			},
			"workspaceInsertBeforeRequest": func(rawValue json.RawMessage) []connection.ValidationIssue {
				_, issues := apiproxy.DecodeWorkspaceInsertBeforeRequest(rawValue)
				return issues
			},
			"workspaceInsertSessionBeforeRequest": func(rawValue json.RawMessage) []connection.ValidationIssue {
				_, issues := apiproxy.DecodeWorkspaceInsertSessionBeforeRequest(rawValue)
				return issues
			},
			"workspaceArchiveSessionRequest": func(rawValue json.RawMessage) []connection.ValidationIssue {
				_, issues := apiproxy.DecodeWorkspaceArchiveSessionRequest(rawValue)
				return issues
			},
		}
		for suiteName, decodePayload := range decoders {
			for _, candidate := range fixtureData.Suites[suiteName] {
				issues := decodePayload(candidate.Input)
				assertAcceptance(t, candidate, len(issues) == 0)
			}
		}
	})
	t.Run("workspace values", func(t *testing.T) {
		vectors := map[string]map[string]json.RawMessage{
			"workspaceListValue":                acceptedByName(t, fixtureData.Suites["workspaceListValue"]),
			"workspaceCreateValue":              acceptedByName(t, fixtureData.Suites["workspaceCreateValue"]),
			"workspaceRenameValue":              acceptedByName(t, fixtureData.Suites["workspaceRenameValue"]),
			"workspaceDeleteValue":              acceptedByName(t, fixtureData.Suites["workspaceDeleteValue"]),
			"workspaceInsertBeforeValue":        acceptedByName(t, fixtureData.Suites["workspaceInsertBeforeValue"]),
			"workspaceInsertSessionBeforeValue": acceptedByName(t, fixtureData.Suites["workspaceInsertSessionBeforeValue"]),
			"workspaceArchiveSessionValue":      acceptedByName(t, fixtureData.Suites["workspaceArchiveSessionValue"]),
		}
		view := apiproxy.WorkspaceView{
			WorkspaceID: "workspace-1", Path: "/workspace", Title: "Workspace",
			SessionIDs: []apiproxy.SessionID{},
			CreatedAt:  "2026-01-01T00:00:00Z", UpdatedAt: "2026-01-01T00:00:00Z",
		}
		assertJSONEqual(t, mustMarshal(t, apiproxy.WorkspaceListValue{
			Items: []apiproxy.WorkspaceView{}, ArchivedSessionIDs: []apiproxy.SessionID{},
		}), vectors["workspaceListValue"]["empty"])
		assertJSONEqual(t, mustMarshal(t, apiproxy.WorkspaceCreateValue{
			Workspace: view, Created: true,
		}), vectors["workspaceCreateValue"]["created"])
		assertJSONEqual(t, mustMarshal(t, apiproxy.WorkspaceRenameValue{
			Workspace: view,
		}), vectors["workspaceRenameValue"]["canonical"])
		assertJSONEqual(t, mustMarshal(t, apiproxy.WorkspaceDeleteValue{
			Deleted: true,
		}), vectors["workspaceDeleteValue"]["deleted"])
		assertJSONEqual(t, mustMarshal(t, apiproxy.WorkspaceInsertBeforeValue{
			WorkspaceIDs: []apiproxy.WorkspaceID{"workspace-2", "workspace-1"},
		}), vectors["workspaceInsertBeforeValue"]["canonical"])
		assertJSONEqual(t, mustMarshal(t, apiproxy.WorkspaceInsertSessionBeforeValue{
			Workspace: view,
		}), vectors["workspaceInsertSessionBeforeValue"]["canonical"])
		assertJSONEqual(t, mustMarshal(t, apiproxy.WorkspaceArchiveSessionValue{
			ArchivedSessionIDs: []apiproxy.SessionID{"session-1"},
		}), vectors["workspaceArchiveSessionValue"]["canonical"])
	})
	t.Run("frames", func(t *testing.T) {
		muxFixtures := acceptedByName(t, fixtureData.Suites["muxFrame"])
		for name, payload := range muxFrameValues() {
			encoded, err := apiproxy.EncodeMuxFrame(payload)
			if err != nil {
				t.Fatalf("encode mux %q: %v", name, err)
			}
			assertJSONEqual(t, encoded, muxFixtures[name])
		}
		hostFixtures := acceptedByName(t, fixtureData.Suites["hostFrame"])
		for name, payload := range hostFrameValues() {
			encoded, err := apiproxy.EncodeHostFrame(payload)
			if err != nil {
				t.Fatalf("encode host %q: %v", name, err)
			}
			assertJSONEqual(t, encoded, hostFixtures[name])
		}
	})
}

func loadManifest(t *testing.T) contractManifest {
	t.Helper()
	var document contractManifest
	loadJSONFile(t, "manifest.json", &document)
	return document
}

func loadFixtures(t *testing.T) fixtureDocument {
	t.Helper()
	var document fixtureDocument
	loadJSONFile(t, "vectors.json", &document)
	return document
}

func loadJSONFile(t *testing.T, name string, target any) {
	t.Helper()
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve contract test path")
	}
	fixturePath := filepath.Join(filepath.Dir(currentFile), "..", "..", "contracts", "deepseek-harness", name)
	content, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(content, target); err != nil {
		t.Fatal(err)
	}
}

func assertAcceptance(t *testing.T, candidate contractVector, actual bool) {
	t.Helper()
	if actual != candidate.Accepted {
		t.Fatalf("%s accepted = %v, source = %v; input = %s", candidate.Name, actual, candidate.Accepted, candidate.Input)
	}
}

func acceptedByName(t *testing.T, candidates []contractVector) map[string]json.RawMessage {
	t.Helper()
	accepted := make(map[string]json.RawMessage)
	for _, candidate := range candidates {
		if candidate.Accepted {
			accepted[candidate.Name] = candidate.Normalized
		}
	}
	return accepted
}

func assertJSONEqual(t *testing.T, actual json.RawMessage, expected json.RawMessage) {
	t.Helper()
	var actualValue any
	if err := json.Unmarshal(actual, &actualValue); err != nil {
		t.Fatalf("decode actual JSON: %v", err)
	}
	var expectedValue any
	if err := json.Unmarshal(expected, &expectedValue); err != nil {
		t.Fatalf("decode expected JSON: %v", err)
	}
	if !reflect.DeepEqual(actualValue, expectedValue) {
		t.Fatalf("actual = %s\nexpected = %s", actual, expected)
	}
}

func mustMarshal(t *testing.T, value any) json.RawMessage {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func muxFrameValues() map[string]apiproxy.MuxFrame {
	return map[string]apiproxy.MuxFrame{
		"session/event": apiproxy.SessionEventFrame{
			SessionID: "session-1",
			Event: apiproxy.SessionEvent{
				Type: "turn/start", Seq: 0, Time: 1, Data: json.RawMessage(`{"turn":1}`),
			},
		},
		"session/subscribed": apiproxy.SessionSubscribedFrame{SessionID: "session-1", LastSeq: -1},
		"approval/requested": apiproxy.ApprovalRequestedFrame{SessionID: "session-1", ApprovalID: "approval-1", ToolName: "bash"},
		"approval/resolved":  apiproxy.ApprovalResolvedFrame{SessionID: "session-1", ApprovalID: "approval-1", Outcome: apiproxy.ApprovalAllowedOnce},
		"question/requested": apiproxy.QuestionRequestedFrame{SessionID: "session-1", Questions: []apiproxy.AskUserQuestionItem{{ID: "question-1", Question: "Continue?"}}},
		"question/resolved":  apiproxy.QuestionResolvedFrame{SessionID: "session-1", QuestionRPCID: "question-rpc-1", Outcome: apiproxy.QuestionAnswered},
		"session/queue":      apiproxy.SessionQueueFrame{SessionID: "session-1", Items: []apiproxy.QueuedInboxItem{}},
		"session/jobs":       apiproxy.SessionJobsFrame{SessionID: "session-1", Jobs: []apiproxy.JobView{}},
		"session/projection": apiproxy.SessionProjectionFrame{SessionID: "session-1", Key: "todos", Value: json.RawMessage(`null`), Seq: 0},
		"stream/error":       streamErrorFrame(),
	}
}

func hostFrameValues() map[string]apiproxy.HostFrame {
	return map[string]apiproxy.HostFrame{
		"host/session-added":   apiproxy.HostSessionAddedFrame{SessionID: "session-1", Blank: true},
		"host/session-removed": apiproxy.HostSessionRemovedFrame{SessionID: "session-1"},
		"host/session-status":  apiproxy.HostSessionStatusFrame{SessionID: "session-1", Running: true},
		"host/agent-error":     apiproxy.HostAgentErrorFrame{SessionID: "session-1", Message: "boom"},
		"host/workspace-changed": apiproxy.HostWorkspaceChangedFrame{Workspace: apiproxy.WorkspaceView{
			WorkspaceID: "workspace-1", Path: "/workspace", Title: "Workspace", SessionIDs: []apiproxy.SessionID{},
			CreatedAt: "2026-01-01T00:00:00Z", UpdatedAt: "2026-01-01T00:00:00Z",
		}},
		"host/workspace-removed":         apiproxy.HostWorkspaceRemovedFrame{WorkspaceID: "workspace-1"},
		"host/workspace-order-changed":   apiproxy.HostWorkspaceOrderChangedFrame{WorkspaceIDs: []apiproxy.WorkspaceID{}},
		"host/archived-sessions-changed": apiproxy.HostArchivedSessionsChangedFrame{ArchivedSessionIDs: []apiproxy.SessionID{}},
		"host/remote-event":              apiproxy.HostRemoteEventFrame{Event: "commands/change", Args: []json.RawMessage{}},
		"stream/error":                   streamErrorFrame(),
	}
}

func streamErrorFrame() apiproxy.StreamErrorFrame {
	return apiproxy.StreamErrorFrame{Error: connection.RPCError{
		Code: connection.ErrorInternal, Message: "boom", Details: json.RawMessage(`{}`),
	}}
}

func encodedMuxFrames(t *testing.T) []string {
	t.Helper()
	names := []string{
		"session/event", "session/subscribed", "approval/requested", "approval/resolved", "question/requested",
		"question/resolved", "session/queue", "session/jobs", "session/projection", "stream/error",
	}
	return encodedFrameNames(t, names, func(name string) (json.RawMessage, error) {
		return apiproxy.EncodeMuxFrame(muxFrameValues()[name])
	})
}

func encodedHostFrames(t *testing.T) []string {
	t.Helper()
	names := []string{
		"host/session-added", "host/session-removed", "host/session-status", "host/agent-error",
		"host/workspace-changed", "host/workspace-removed", "host/workspace-order-changed",
		"host/archived-sessions-changed", "host/remote-event", "stream/error",
	}
	return encodedFrameNames(t, names, func(name string) (json.RawMessage, error) {
		return apiproxy.EncodeHostFrame(hostFrameValues()[name])
	})
}

func encodedFrameNames(t *testing.T, names []string, encode func(string) (json.RawMessage, error)) []string {
	t.Helper()
	encodedNames := make([]string, 0, len(names))
	for _, name := range names {
		encoded, err := encode(name)
		if err != nil {
			t.Fatal(err)
		}
		var discriminant struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(encoded, &discriminant); err != nil {
			t.Fatal(err)
		}
		encodedNames = append(encodedNames, discriminant.Type)
	}
	return encodedNames
}
