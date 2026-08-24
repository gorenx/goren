package basic

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/compaction/toolresultpruner"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/llm/tokenmeter"
	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/session"
)

const fixtureProvider = "fixture-provider"
const fixtureModel = "fixture-model"

type meterStub struct {
	measure       func(context.Context, *session.Session, *session.EpochHeader) (tokenmeter.Measurement, error)
	estimate      int64
	estimateError error
}

func (stub *meterStub) Measure(
	requestContext context.Context,
	conversation *session.Session,
	header *session.EpochHeader,
) (tokenmeter.Measurement, error) {
	if stub.measure != nil {
		return stub.measure(requestContext, conversation, header)
	}
	return pricedSurface(conversation, 100, 0), nil
}

func (stub *meterStub) EstimateMessage(llm.Message) (int64, error) {
	if stub.estimateError != nil {
		return 0, stub.estimateError
	}
	if stub.estimate == 0 {
		return 10, nil
	}
	return stub.estimate, nil
}

func pricedSurface(
	conversation *session.Session,
	perNode int64,
	envelope int64,
) tokenmeter.Measurement {
	_, surface := conversation.ReadCut()
	nodes := make([]tokenmeter.SurfaceNode, len(surface.Nodes))
	surfaceTokens := int64(0)
	for nodeIndex, sequence := range surface.Nodes {
		nodes[nodeIndex] = tokenmeter.SurfaceNode{
			Seq:    sequence,
			Tokens: perNode,
		}
		surfaceTokens += perNode
	}
	return tokenmeter.Measurement{
		LogRevision: conversation.Seq(),
		Baseline: tokenmeter.Baseline{
			Kind:   tokenmeter.BaselineEstimated,
			Tokens: envelope,
		},
		TotalTokens:   surfaceTokens + envelope,
		SurfaceTokens: surfaceTokens,
		Nodes:         nodes,
	}
}

type runtimeStub struct {
	llm.LlmRuntime

	mutex         sync.Mutex
	resolved      llm.ResolvedModelInfo
	resolveErr    error
	beforeResolve func()
	chunks        []llm.StreamChunk
	streamErr     error
	nilStream     bool
	beforeCall    func()
	requests      []llm.GenerateOptions
}

func (stub *runtimeStub) ResolveModelInfo(
	_ context.Context,
	providerRoute string,
	modelID string,
) (llm.ResolvedModelInfo, error) {
	if stub.beforeResolve != nil {
		stub.beforeResolve()
	}
	if stub.resolveErr != nil {
		return llm.ResolvedModelInfo{}, stub.resolveErr
	}
	resolved := stub.resolved
	if resolved.Provider == "" {
		resolved.Provider = providerRoute
	}
	if resolved.ID == "" {
		resolved.ID = modelID
	}
	if resolved.Name == "" {
		resolved.Name = modelID
	}
	return resolved, nil
}

func (stub *runtimeStub) Stream(
	_ context.Context,
	options llm.GenerateOptions,
) (llm.ChunkStream, error) {
	if stub.beforeCall != nil {
		stub.beforeCall()
	}
	if stub.streamErr != nil {
		return nil, stub.streamErr
	}
	stub.mutex.Lock()
	stub.requests = append(stub.requests, options)
	stub.mutex.Unlock()
	if stub.nilStream {
		return nil, nil
	}
	return llm.NewSliceStream(stub.chunks)
}

func (stub *runtimeStub) requestValues() []llm.GenerateOptions {
	stub.mutex.Lock()
	defer stub.mutex.Unlock()
	return append([]llm.GenerateOptions(nil), stub.requests...)
}

type liveStoreStub struct {
	session.LiveStore

	mutex      sync.Mutex
	flushes    int
	flushError error
}

func (stub *liveStoreStub) Flush(
	requestContext context.Context,
	conversation *session.Session,
) error {
	if requestContext == nil || conversation == nil {
		return errors.New("fixture: flush inputs are required")
	}
	stub.mutex.Lock()
	stub.flushes++
	stub.mutex.Unlock()
	return stub.flushError
}

func (stub *liveStoreStub) flushCount() int {
	stub.mutex.Lock()
	defer stub.mutex.Unlock()
	return stub.flushes
}

type prunerStub struct {
	toolresultpruner.Pruner

	prune func(context.Context, *session.Session) (toolresultpruner.Result, error)
	calls int
}

func (stub *prunerStub) PruneSession(
	requestContext context.Context,
	conversation *session.Session,
) (toolresultpruner.Result, error) {
	stub.calls++
	if stub.prune == nil {
		return toolresultpruner.Result{}, nil
	}
	return stub.prune(requestContext, conversation)
}

type maintenanceStub struct {
	run              bool
	returnErr        error
	operationContext context.Context
}

func (stub *maintenanceStub) RunMaintenance(
	requestContext context.Context,
	operation func(context.Context) error,
) error {
	if !stub.run {
		return stub.returnErr
	}
	if operation == nil {
		return errors.New("fixture: maintenance operation is nil")
	}
	selectedContext := requestContext
	if stub.operationContext != nil {
		selectedContext = stub.operationContext
	}
	if err := operation(selectedContext); err != nil {
		return err
	}
	return stub.returnErr
}

type agentStub struct {
	agent.Agent
	base         plugin.Base
	identifier   session.SessionID
	conversation *session.Session
	options      agent.Options
	maintenance  *maintenanceStub
}

func (stub *agentStub) RuntimePlugin() *plugin.Base {
	return &stub.base
}

func (stub *agentStub) ID() session.SessionID {
	return stub.identifier
}

func (stub *agentStub) SessionValue() *session.Session {
	return stub.conversation
}

func (stub *agentStub) OptionsValue() agent.Options {
	return stub.options
}

func (stub *agentStub) RunMaintenance(
	requestContext context.Context,
	operation func(context.Context) error,
) error {
	if stub.maintenance == nil {
		return errors.New("fixture: maintenance behavior is nil")
	}
	return stub.maintenance.RunMaintenance(requestContext, operation)
}

func newRuntimeStub(summary string, contextWindow int) *runtimeStub {
	return &runtimeStub{
		resolved: llm.ResolvedModelInfo{
			Context: &llm.ModelContext{
				ContextWindow: contextWindow,
			},
		},
		chunks: []llm.StreamChunk{
			llm.ReasoningDeltaChunk{
				Index: 0,
				Text:  "private reasoning",
			},
			llm.TextDeltaChunk{
				Index: 1,
				Text:  summary,
			},
			llm.UsageChunk{
				Usage: llm.TokenUsage{
					InputTokens:  40,
					OutputTokens: 5,
				},
			},
			llm.FinishChunk{
				Reason: llm.StopFinish{},
			},
		},
	}
}

func newBoundCompaction(
	testingContext *testing.T,
	settings Config,
	runtimeValue *runtimeStub,
	meterValue *meterStub,
	storeValue *liveStoreStub,
	prunerValue toolresultpruner.Pruner,
) *Compaction {
	testingContext.Helper()
	resolved, err := ResolveConfig(settings)
	if err != nil {
		testingContext.Fatal(err)
	}
	implementation := newCompaction(resolved)
	implementation.bind(runtimeValue, storeValue, meterValue, prunerValue)
	testingContext.Cleanup(implementation.release)
	return implementation
}

func conversationFixture(
	testingContext *testing.T,
	closedTurns int,
	text string,
) *session.Session {
	testingContext.Helper()
	conversation, err := session.New(
		session.SessionID(fmt.Sprintf("compaction-%d", closedTurns)),
		session.CreateOptions{},
	)
	if err != nil {
		testingContext.Fatal(err)
	}
	populateConversationFixture(
		testingContext,
		conversation,
		closedTurns,
		text,
	)
	return conversation
}

func populateConversationFixture(
	testingContext *testing.T,
	conversation *session.Session,
	closedTurns int,
	text string,
) {
	testingContext.Helper()
	for turn := 1; turn <= closedTurns; turn++ {
		if _, err := session.Append(
			conversation,
			session.TurnStarted,
			session.TurnStart{
				Turn: int64(turn),
			},
		); err != nil {
			testingContext.Fatal(err)
		}
		userInput, err := llm.NewUserMessage(llm.UserMessageInput{
			Content: []llm.ContentBlock{
				llm.NewTextBlock(fmt.Sprintf("%s user %d", text, turn)),
			},
			Source: llm.UserMessageSource{},
		})
		if err != nil {
			testingContext.Fatal(err)
		}
		if _, err := session.AppendSurface(
			conversation,
			session.UserMessageAdded,
			userInput,
			session.SurfaceIntent{
				Operation: session.SurfaceAppend(),
			},
		); err != nil {
			testingContext.Fatal(err)
		}
		if turn == 1 {
			if _, err := session.Append(
				conversation,
				session.RequestHeaderSet,
				session.RequestHeaderSnapshot{
					Header: session.EpochHeader{
						Config: llm.CallConfig{
							Provider: fixtureProvider,
							Model:    fixtureModel,
						},
					},
					Reason: session.RequestHeaderInitial,
				},
			); err != nil {
				testingContext.Fatal(err)
			}
		}
		assistantOutput, err := llm.NewAssistantMessage(llm.AssistantMessageInput{
			Content: []llm.ContentBlock{
				llm.NewTextBlock(fmt.Sprintf("%s assistant %d", text, turn)),
			},
			Source: llm.ModelMessageSource{
				Provider: fixtureProvider,
				Model:    fixtureModel,
			},
		})
		if err != nil {
			testingContext.Fatal(err)
		}
		if _, err := session.AppendSurface(
			conversation,
			session.AssistantMessaged,
			session.AssistantMessage{
				Turn:    int64(turn),
				Step:    1,
				Message: assistantOutput,
			},
			session.SurfaceIntent{
				Operation: session.SurfaceAppend(),
			},
		); err != nil {
			testingContext.Fatal(err)
		}
		if _, err := session.Append(
			conversation,
			session.TurnEnded,
			session.TurnEnd{
				Turn:   int64(turn),
				Reason: session.TurnCompleted{},
			},
		); err != nil {
			testingContext.Fatal(err)
		}
	}
	if _, err := session.Append(
		conversation,
		session.TurnStarted,
		session.TurnStart{
			Turn: int64(closedTurns + 1),
		},
	); err != nil {
		testingContext.Fatal(err)
	}
}

func textFromBlocks(blocks []llm.ContentBlock) string {
	combined := ""
	for _, blockValue := range blocks {
		textValue, supported := blockValue.(llm.PlainTextContent)
		if !supported {
			continue
		}
		plainText, present := textValue.PlainText()
		if present {
			combined += plainText
		}
	}
	return combined
}

var _ tokenmeter.Meter = (*meterStub)(nil)
var _ llm.LlmRuntime = (*runtimeStub)(nil)
var _ session.LiveStore = (*liveStoreStub)(nil)
var _ toolresultpruner.Pruner = (*prunerStub)(nil)
var _ agent.Agent = (*agentStub)(nil)
