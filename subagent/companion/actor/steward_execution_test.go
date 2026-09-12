package companionactor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorenx/goren/agent"
	agentactor "github.com/gorenx/goren/agent/actor"
	agentregistry "github.com/gorenx/goren/agent/registry"
	loopactor "github.com/gorenx/goren/agentloop/actor"
	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/llm"
	llmactor "github.com/gorenx/goren/llm/actor"
	"github.com/gorenx/goren/llm/modelconfig"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/tools/tool"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/reentrancy"
)

// delegationModel replaces only provider output. Both Agents execute real model
// calls and tools; gates choose when the report reaches the parent's ordinary Inbox.
type delegationModel struct {
	mu             sync.Mutex
	requests       map[string][]llm.GenerateOptions
	parentWaiting  chan struct{}
	allowReport    chan struct{}
	reportAccepted chan struct{}
	finished       chan struct{}
	parentRelease  <-chan struct{}
}

func (backend *delegationModel) Stream(ctx context.Context, input llm.GenerateOptions) (llm.ChunkStream, error) {
	backend.mu.Lock()
	ordinal := len(backend.requests[input.SessionID])
	backend.requests[input.SessionID] = append(backend.requests[input.SessionID], input)
	backend.mu.Unlock()
	if input.SessionID == "managed-parent" {
		switch ordinal {
		case 0:
			if len(input.Tools) != 2 || !hasTool(input.Tools, "list_agents") || !hasTool(input.Tools, "send_message") {
				return nil, errors.New("parent did not receive its explicit tool set")
			}
			return modelTool("discover", "list_agents", `{}`), nil
		case 1:
			listing, err := modelResult(input.Messages, "discover")
			if err != nil {
				return nil, err
			}
			fields := strings.Fields(listing)
			if len(fields) != 4 || fields[1] != "[idle]" || fields[3] != "reviewer" {
				return nil, fmt.Errorf("unexpected Companion listing: %s", listing)
			}
			arguments, err := json.Marshal(map[string]string{
				"subagent_id": fields[0],
				"message":     "review the current change",
			})
			if err != nil {
				return nil, err
			}
			return modelTool("delegate", "send_message", string(arguments)), nil
		case 2:
			if _, err := modelResult(input.Messages, "delegate"); err != nil {
				return nil, err
			}
			close(backend.parentWaiting)
			if backend.parentRelease != nil {
				select {
				case <-backend.parentRelease:
				case <-ctx.Done():
					return nil, context.Cause(ctx)
				}
			}
			return modelText("Review delegated."), nil
		case 3:
			if _, err := modelSource(input.Messages, "subagent-report", "review complete"); err != nil {
				return nil, err
			}
			close(backend.finished)
			return modelText("The review is complete."), nil
		}
	} else {
		switch ordinal {
		case 0:
			if len(input.Tools) != 1 || !hasTool(input.Tools, "report") {
				return nil, errors.New("Companion inherited parent tools implicitly")
			}
			if _, err := modelSource(input.Messages, "coordinator", "review the current change"); err != nil {
				return nil, err
			}
			select {
			case <-backend.allowReport:
			case <-ctx.Done():
				return nil, context.Cause(ctx)
			}
			return modelTool("finding", "report", `{"output":"review complete"}`), nil
		case 1:
			if _, err := modelResult(input.Messages, "finding"); err != nil {
				return nil, err
			}
			close(backend.reportAccepted)
			return modelText("Reported the review."), nil
		}
	}
	return nil, fmt.Errorf("unexpected model call %s/%d", input.SessionID, ordinal)
}

func hasTool(definitions []llm.ToolSchema, name string) bool {
	return slices.ContainsFunc(definitions, func(entry llm.ToolSchema) bool { return entry.Name == name })
}

func modelResult(messages []agentmessage.Message, identifier agentmessage.CallID) (string, error) {
	for _, message := range messages {
		for _, block := range message.ContentValue() {
			result, ok := block.(agentmessage.ToolResultBlock)
			if !ok || result.ToolCallID != identifier {
				continue
			}
			if result.IsError {
				return "", fmt.Errorf("tool %s failed: %+v", identifier, result.Content)
			}
			var output strings.Builder
			for _, item := range result.Content {
				if value, ok := item.(agentmessage.TextBlock); ok {
					output.WriteString(value.Text)
				}
			}
			return output.String(), nil
		}
	}
	return "", fmt.Errorf("model did not receive result for %s", identifier)
}

func modelSource(messages []agentmessage.Message, kind string, content string) (agentmessage.Message, error) {
	for _, message := range messages {
		encoded, err := json.Marshal(message.SourceValue())
		if err != nil {
			return nil, err
		}
		var origin struct {
			Kind            string `json:"kind"`
			SenderSessionID string `json:"senderSessionId"`
		}
		if err := json.Unmarshal(encoded, &origin); err != nil {
			return nil, err
		}
		if origin.Kind != kind || origin.SenderSessionID == "" {
			continue
		}
		for _, block := range message.ContentValue() {
			if text, ok := block.(agentmessage.TextBlock); ok && text.Text == content {
				return message, nil
			}
		}
	}
	return nil, fmt.Errorf("model did not receive %s input %q", kind, content)
}

type modelStream struct {
	chunks []llm.StreamChunk
}

func (flow *modelStream) Next(ctx context.Context) (llm.StreamChunk, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	if len(flow.chunks) == 0 {
		return nil, false, nil
	}
	chunk := flow.chunks[0]
	flow.chunks = flow.chunks[1:]
	return chunk, true, nil
}

func (*modelStream) Close(context.Context) error { return nil }

func modelTool(identifier agentmessage.CallID, name string, arguments string) llm.ChunkStream {
	return &modelStream{
		chunks: []llm.StreamChunk{
			llm.BlockStartChunk{
				BlockType: "tool-call",
			},
			llm.ToolCallDeltaChunk{
				ID:             identifier,
				Name:           &name,
				ArgumentsDelta: arguments,
			},
			llm.BlockEndChunk{
				Block: agentmessage.ToolCallBlock{
					ID:        identifier,
					Name:      name,
					Arguments: arguments,
				},
			},
			llm.FinishChunk{
				Reason: llm.ToolCallsFinish{},
			},
		},
	}
}

func modelText(value string) llm.ChunkStream {
	return &modelStream{
		chunks: []llm.StreamChunk{
			llm.BlockStartChunk{
				BlockType: "text",
			},
			llm.TextDeltaChunk{
				Text: value,
			},
			llm.BlockEndChunk{
				Block: agentmessage.NewTextBlock(value),
			},
			llm.FinishChunk{
				Reason: llm.StopFinish{},
			},
		},
	}
}

func TestCompanionModelDelegationAndReport(t *testing.T) {
	for _, arrival := range []string{"active", "idle", "draining"} {
		t.Run(arrival, func(t *testing.T) {
			backend := &delegationModel{
				requests:       make(map[string][]llm.GenerateOptions),
				parentWaiting:  make(chan struct{}),
				allowReport:    make(chan struct{}),
				reportAccepted: make(chan struct{}),
				finished:       make(chan struct{}),
			}
			if arrival != "idle" {
				backend.parentRelease = backend.reportAccepted
			}
			fixture, _, _ := messagingFixture(t, func(current *stewardship) tool.Providers {
				target, err := goakt.GrainOf[*llmactor.Grain](t.Context(), current.system, "execution-models",
					goakt.WithLongLivedGrain(),
					goakt.WithGrainReentrancy(reentrancy.New(reentrancy.WithMode(reentrancy.AllowAll))),
				)
				if err != nil {
					t.Fatal(err)
				}
				catalog, err := modelconfig.NewCatalog(modelconfig.Definition{
					Providers: []modelconfig.Provider{
						{
							ID: "test",
						},
					},
					Default: llm.CallConfig{
						Provider: "test",
						Model:    "review",
					},
				})
				if err != nil {
					t.Fatal(err)
				}
				if result := request[llmactor.Completion](t, current.system, target, llmactor.InitializeModels{
					Catalog: catalog,
				}); result.Err != nil {
					t.Fatal(result.Err)
				}
				if result := request[llmactor.Registered](t, current.system, target, llmactor.RegisterAdapter{
					Providers: []string{"test"},
					Adapter:   backend,
				}); result.Err != nil {
					t.Fatal(result.Err)
				}
				pool, err := llmactor.SpawnPool(t.Context(), current.system, "execution-models", 2, llmactor.InitializeWorker{
					LLM: target,
				})
				if err != nil {
					t.Fatal(err)
				}
				current.input.ModelConfig = target
				current.input.ModelRouter = pool
				return nil
			})
			endpoint, sender, input := installParent(t, fixture, fixture.authority)
			awaitInstallation(t, fixture, endpoint, input.Creation, Ready)
			if _, err := sender.Create(t.Context(), Create{
				Name:    "reviewer",
				Enabled: true,
			}); err != nil {
				t.Fatal(err)
			}
			child := awaitPhase(t, sender, "reviewer", Ready)
			message, err := agentmessage.NewUserMessage(agentmessage.UserMessageInput{
				Content: []agentmessage.ContentBlock{agentmessage.NewTextBlock("Ask your reviewer to review the change.")},
				Source:  agentmessage.UserMessageSource{},
			})
			if err != nil {
				t.Fatal(err)
			}
			parent := request[agentregistry.Ready](t, fixture.system, fixture.authority, agentregistry.FindAgent{
				AgentID: input.Creation.AgentID,
			})
			if parent.Err != nil {
				t.Fatal(parent.Err)
			}
			if accepted := request[loopactor.Changed](t, fixture.system, parent.Executor, loopactor.Followup{
				CreationID: input.Creation.CreationID,
				Message:    message,
			}); accepted.Err != nil {
				t.Fatal(accepted.Err)
			}
			awaitModel(t, backend.parentWaiting, "parent delegation")
			if arrival == "idle" {
				awaitAgentIdle(t, fixture, input.Creation)
			} else if fixture.activity(t, input.Creation).Status != agent.StatusRunning {
				t.Fatal("parent stopped before receiving the report")
			}
			if arrival == "draining" {
				if _, err := sender.Remove(t.Context(), Remove{
					Name:             child.Companion.Name,
					ExpectedRevision: child.Companion.Revision,
				}); err != nil {
					t.Fatal(err)
				}
				awaitPhase(t, sender, child.Companion.Name, Draining)
			}
			close(backend.allowReport)
			if arrival == "idle" {
				awaitModel(t, backend.reportAccepted, "report queued while parent is idle")
				awaitAgentIdle(t, fixture, child.Creation)
				current := fixture.activity(t, input.Creation)
				if current.Status != agent.StatusIdle || len(current.Pending.NextStep) != 1 {
					t.Fatalf("report woke the parent or was not queued: %+v", current)
				}
				backend.mu.Lock()
				calls := len(backend.requests[string(input.Creation.AgentID)])
				backend.mu.Unlock()
				if calls != 3 {
					t.Fatalf("report started an unsolicited model call: %d", calls)
				}
				followup, err := agentmessage.NewUserMessage(agentmessage.UserMessageInput{
					Content: []agentmessage.ContentBlock{agentmessage.NewTextBlock("Continue with the review result.")},
					Source:  agentmessage.UserMessageSource{},
				})
				if err != nil {
					t.Fatal(err)
				}
				if accepted := request[loopactor.Changed](t, fixture.system, parent.Executor, loopactor.Followup{
					CreationID: input.Creation.CreationID,
					Message:    followup,
				}); accepted.Err != nil {
					t.Fatal(accepted.Err)
				}
			}
			awaitModel(t, backend.finished, "parent continuation after report")
			awaitAgentIdle(t, fixture, input.Creation)
			if arrival == "draining" {
				awaitPhase(t, sender, child.Companion.Name, Stopped)
			} else {
				awaitAgentIdle(t, fixture, child.Creation)
			}
			backend.mu.Lock()
			parentCalls := slices.Clone(backend.requests[string(input.Creation.AgentID)])
			childCalls := slices.Clone(backend.requests[string(child.Creation.AgentID)])
			backend.mu.Unlock()
			if len(parentCalls) != 4 || len(childCalls) != 2 {
				t.Fatalf("unexpected model calls: parent=%d child=%d", len(parentCalls), len(childCalls))
			}
			task, err := modelSource(childCalls[0].Messages, "coordinator", "review the current change")
			if err != nil {
				t.Fatal(err)
			}
			finding, err := modelSource(parentCalls[3].Messages, "subagent-report", "review complete")
			if err != nil {
				t.Fatal(err)
			}
			for _, expected := range []struct {
				creation agentregistry.Creation
				message  agentmessage.Message
				sender   session.SessionID
				kind     string
			}{
				{
					creation: input.Creation,
					message:  finding,
					sender:   child.Creation.AgentID,
					kind:     "subagent-report",
				},
				{
					creation: child.Creation,
					message:  task,
					sender:   input.Creation.AgentID,
					kind:     "coordinator",
				},
			} {
				origin, err := json.Marshal(expected.message.SourceValue())
				if err != nil {
					t.Fatal(err)
				}
				if string(origin) != fmt.Sprintf(`{"kind":%q,"form":"relay","senderSessionId":%q}`, expected.kind, expected.sender) {
					t.Fatalf("incorrect model-visible sender: %s", origin)
				}
				var persisted agentmessage.UserMessage
				if arrival == "draining" && expected.creation == child.Creation {
					persisted = fixture.storedMessage(t, expected.creation, expected.message.StableID())
				} else {
					persisted = fixture.persistedMessage(t, expected.creation, expected.message.StableID())
				}
				before, _ := json.Marshal(expected.message)
				after, _ := json.Marshal(persisted)
				if string(before) != string(after) {
					t.Fatal("model input differs from durable message")
				}
				assertCompletedTurns(t, fixture, expected.creation)
			}
			closed := request[agentactor.Closed](t, fixture.system, fixture.authority, agentregistry.CloseAgent{
				Creation: input.Creation,
			})
			if closed.Err != nil {
				t.Fatal(closed.Err)
			}
			awaitInstallation(t, fixture, endpoint, input.Creation, Stopped)
			if remaining := request[agentregistry.Ready](t, fixture.system, fixture.authority, agentregistry.FindAgent{
				AgentID: child.Creation.AgentID,
			}); remaining.Err == nil {
				t.Fatal("parent closure retained the Companion")
			}
		})
	}
}

func awaitModel(t *testing.T, reached <-chan struct{}, label string) {
	t.Helper()
	select {
	case <-reached:
	case <-time.After(5 * time.Second):
		t.Fatalf("model did not reach %s", label)
	}
}

func awaitAgentIdle(t *testing.T, fixture *stewardship, expected agentregistry.Creation) {
	t.Helper()
	ready := request[agentregistry.Ready](t, fixture.system, fixture.authority, agentregistry.FindAgent{
		AgentID: expected.AgentID,
	})
	if ready.Err != nil {
		t.Fatal(ready.Err)
	}
	result := request[loopactor.Completion](t, fixture.system, ready.Executor, loopactor.WhenIdle{
		CreationID: expected.CreationID,
	})
	if result.Err != nil {
		t.Fatal(result.Err)
	}
}

func assertCompletedTurns(t *testing.T, fixture *stewardship, expected agentregistry.Creation) {
	t.Helper()
	stored, err := fixture.creator.storage.Load(t.Context(), expected.AgentID)
	if err != nil {
		t.Fatal(err)
	}
	if stored == nil {
		t.Fatal("Session was not persisted")
	}
	completed := 0
	for _, event := range stored.Events {
		if event.Type != session.TurnEndEventName {
			continue
		}
		var ending session.TurnEnd
		if err := json.Unmarshal(event.Data, &ending); err != nil {
			t.Fatal(err)
		}
		if ending.Reason.TurnEndKind() != "completed" {
			t.Fatalf("%s ended abnormally: %s", expected.AgentID, event.Data)
		}
		completed++
	}
	if completed == 0 {
		t.Fatal("no completed turn")
	}
}
