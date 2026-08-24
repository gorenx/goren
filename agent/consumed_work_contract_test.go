//go:build contract

package agent_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	agentcore "github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/session"
	contractfixture "github.com/gorenx/goren/tests/contract/fixture"
)

type consumedWorkContractObservation struct {
	Name         string  `json:"name"`
	EndTurn      *int64  `json:"endTurn"`
	EndKind      *string `json:"endKind"`
	DroppedUnrun bool    `json:"droppedUnrun"`
}

func TestPinnedSourceConsumedWorkMatchesGo(t *testing.T) {
	repositoryRoot, sourceRoot := contractfixture.Paths(t)
	sourceCommit := contractfixture.SourceCommit(
		t,
		filepath.Join(
			repositoryRoot,
			"subagent",
			"testdata",
			"source-baseline.json",
		),
	)
	requestContext, cancelRequest := context.WithTimeout(
		context.Background(),
		30*time.Second,
	)
	defer cancelRequest()
	sourceOutput, sourceErr := contractfixture.RunTypeScript(
		requestContext,
		sourceRoot,
		nil,
		filepath.Join(
			repositoryRoot,
			"tests",
			"contract",
			"typescript",
			"agent-consumed-work.ts",
		),
		sourceRoot,
		sourceCommit,
	)
	if sourceErr != nil {
		t.Fatal(sourceErr)
	}
	var sourceObservations []consumedWorkContractObservation
	if decodeErr := json.Unmarshal(sourceOutput, &sourceObservations); decodeErr != nil {
		t.Fatal(decodeErr)
	}
	goObservations := []consumedWorkContractObservation{
		observeConsumedWork(t, "latest-stepped", func(conversation session.Context) {
			appendContractSteppedTurn(t, conversation, 1, session.TurnCompleted{})
			appendContractSteppedTurn(t, conversation, 2, session.TurnMaxTokens{})
		}),
		observeConsumedWork(t, "claimed-pre-step-error", func(conversation session.Context) {
			appendContractSteppedTurn(t, conversation, 1, session.TurnCompleted{})
			appendContractTurnStart(t, conversation, 2)
			appendContractClaim(t, conversation)
			appendContractTurnEnd(t, conversation, 2, session.TurnError{
				Error: llm.LlmFailure{
					Code:    "MODEL",
					Message: "failed",
				},
			})
		}),
		observeConsumedWork(t, "unclaimed-later-turns", func(conversation session.Context) {
			appendContractSteppedTurn(t, conversation, 1, session.TurnCompleted{})
			appendContractTurnStart(t, conversation, 2)
			appendContractTurnEnd(t, conversation, 2, session.TurnBlocked{})
		}),
		observeConsumedWork(t, "completed-empty-claim", func(conversation session.Context) {
			appendContractSteppedTurn(t, conversation, 1, session.TurnCompleted{})
			appendContractTurnStart(t, conversation, 2)
			appendContractClaim(t, conversation)
			appendContractTurnEnd(t, conversation, 2, session.TurnCompleted{})
		}),
		observeConsumedWork(t, "mid-turn-suffix", func(conversation session.Context) {
			appendContractClaim(t, conversation)
			appendContractTurnEnd(t, conversation, 2, session.TurnAborted{
				Reason: session.UserCancelCause{},
			})
		}),
		observeConsumedWork(t, "dropped-unrun", func(conversation session.Context) {
			appendContractCancellation(t, conversation, nil)
		}),
		observeConsumedWork(t, "replacement-stays-pending", func(conversation session.Context) {
			appendContractCancellation(
				t,
				conversation,
				[]llm.UserMessage{
					contractMessage(t, "replacement"),
				},
			)
		}),
		observeConsumedWork(t, "later-turn-absorbs-drop", func(conversation session.Context) {
			appendContractCancellation(t, conversation, nil)
			appendContractSteppedTurn(t, conversation, 1, session.TurnCompleted{})
		}),
	}
	if !reflect.DeepEqual(goObservations, sourceObservations) {
		t.Fatalf(
			"Go observations = %#v, source observations = %#v",
			goObservations,
			sourceObservations,
		)
	}
}

func observeConsumedWork(
	t *testing.T,
	name string,
	buildEvents func(session.Context),
) consumedWorkContractObservation {
	t.Helper()
	conversation, createErr := session.New(
		session.SessionID(name),
		session.CreateOptions{},
	)
	if createErr != nil {
		t.Fatal(createErr)
	}
	buildEvents(conversation)
	work, foldErr := agentcore.FoldConsumedWork(conversation.Events())
	if foldErr != nil {
		t.Fatal(foldErr)
	}
	observation := consumedWorkContractObservation{
		Name:         name,
		DroppedUnrun: work.DroppedUnrun,
	}
	if work.End != nil {
		turnSnapshot := work.End.Turn
		kindSnapshot := work.End.Reason.TurnEndKind()
		observation.EndTurn = &turnSnapshot
		observation.EndKind = &kindSnapshot
	}
	return observation
}

func appendContractSteppedTurn(
	t *testing.T,
	conversation session.Context,
	turnNumber int64,
	ending session.TurnEndReason,
) {
	t.Helper()
	appendContractTurnStart(t, conversation, turnNumber)
	appendContractClaim(t, conversation)
	{
		var committedEvent session.Event
		var writeErr error
		draft, draftErr := session.NewEventDraft(session.StepStarted,
			session.StepPosition{
				Turn: turnNumber,
				Step: 1,
			})
		writeErr = draftErr
		if draftErr == nil {
			receipt, commitErr := conversation.Commit(context.Background(), session.Batch(draft))
			writeErr = commitErr
			if commitErr == nil {
				committedEvent = receipt.Events[0]
			}
		}
		if _, appendErr := committedEvent, writeErr; appendErr != nil {
			t.Fatal(appendErr)
		}
	}
	appendContractTurnEnd(t, conversation, turnNumber, ending)
}

func appendContractTurnStart(
	t *testing.T,
	conversation session.Context,
	turnNumber int64,
) {
	t.Helper()
	{
		var committedEvent session.Event
		var writeErr error
		draft, draftErr := session.NewEventDraft(session.TurnStarted,
			session.TurnStart{
				Turn: turnNumber,
			})
		writeErr = draftErr
		if draftErr == nil {
			receipt, commitErr := conversation.Commit(context.Background(), session.Batch(draft))
			writeErr = commitErr
			if commitErr == nil {
				committedEvent = receipt.Events[0]
			}
		}
		if _, appendErr := committedEvent, writeErr; appendErr != nil {
			t.Fatal(appendErr)
		}
	}
}

func appendContractTurnEnd(
	t *testing.T,
	conversation session.Context,
	turnNumber int64,
	ending session.TurnEndReason,
) {
	t.Helper()
	{
		var committedEvent session.Event
		var writeErr error
		draft, draftErr := session.NewEventDraft(session.TurnEnded,
			session.TurnEnd{
				Turn:   turnNumber,
				Reason: ending,
			})
		writeErr = draftErr
		if draftErr == nil {
			receipt, commitErr := conversation.Commit(context.Background(), session.Batch(draft))
			writeErr = commitErr
			if commitErr == nil {
				committedEvent = receipt.Events[0]
			}
		}
		if _, appendErr := committedEvent, writeErr; appendErr != nil {
			t.Fatal(appendErr)
		}
	}
}

func appendContractClaim(
	t *testing.T,
	conversation session.Context,
) {
	t.Helper()
	removedCount := 1
	{
		var committedEvent session.Event
		var writeErr error
		draft, draftErr := session.NewEventDraft(agentcore.InboxSpliced,
			agentcore.InboxSplice{
				Target:       agentcore.NextTurn,
				Start:        0,
				RemovedCount: &removedCount,
				Inserted:     []llm.UserMessage{},
			})
		writeErr = draftErr
		if draftErr == nil {
			receipt, commitErr := conversation.Commit(context.Background(), session.Batch(draft))
			writeErr = commitErr
			if commitErr == nil {
				committedEvent = receipt.Events[0]
			}
		}
		if _, appendErr := committedEvent, writeErr; appendErr != nil {
			t.Fatal(appendErr)
		}
	}
}

func appendContractCancellation(
	t *testing.T,
	conversation session.Context,
	replacementMessages []llm.UserMessage,
) {
	t.Helper()
	if replacementMessages == nil {
		replacementMessages = []llm.UserMessage{}
	}
	removedCount := 1
	{
		var committedEvent session.Event
		var writeErr error
		draft, draftErr := session.NewEventDraft(agentcore.InboxSpliced,
			agentcore.InboxSplice{
				Target:       agentcore.NextTurn,
				Start:        0,
				RemovedCount: &removedCount,
				Inserted:     replacementMessages,
				Outcome:      agentcore.InboxCanceled,
			})
		writeErr = draftErr
		if draftErr == nil {
			receipt, commitErr := conversation.Commit(context.Background(), session.Batch(draft))
			writeErr = commitErr
			if commitErr == nil {
				committedEvent = receipt.Events[0]
			}
		}
		if _, appendErr := committedEvent, writeErr; appendErr != nil {
			t.Fatal(appendErr)
		}
	}
}

func contractMessage(t *testing.T, text string) llm.UserMessage {
	t.Helper()
	messageValue, messageErr := llm.NewUserMessage(
		llm.UserMessageInput{
			Content: []llm.ContentBlock{
				llm.NewTextBlock(text),
			},
			Source: llm.UserMessageSource{},
		},
	)
	if messageErr != nil {
		t.Fatal(messageErr)
	}
	return messageValue
}
