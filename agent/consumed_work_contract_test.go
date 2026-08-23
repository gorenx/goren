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
		observeConsumedWork(t, "latest-stepped", func(conversation *session.Session) {
			appendContractSteppedTurn(t, conversation, 1, session.TurnCompleted{})
			appendContractSteppedTurn(t, conversation, 2, session.TurnMaxTokens{})
		}),
		observeConsumedWork(t, "claimed-pre-step-error", func(conversation *session.Session) {
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
		observeConsumedWork(t, "unclaimed-later-turns", func(conversation *session.Session) {
			appendContractSteppedTurn(t, conversation, 1, session.TurnCompleted{})
			appendContractTurnStart(t, conversation, 2)
			appendContractTurnEnd(t, conversation, 2, session.TurnBlocked{})
		}),
		observeConsumedWork(t, "completed-empty-claim", func(conversation *session.Session) {
			appendContractSteppedTurn(t, conversation, 1, session.TurnCompleted{})
			appendContractTurnStart(t, conversation, 2)
			appendContractClaim(t, conversation)
			appendContractTurnEnd(t, conversation, 2, session.TurnCompleted{})
		}),
		observeConsumedWork(t, "mid-turn-suffix", func(conversation *session.Session) {
			appendContractClaim(t, conversation)
			appendContractTurnEnd(t, conversation, 2, session.TurnAborted{
				Reason: session.UserCancelCause{},
			})
		}),
		observeConsumedWork(t, "dropped-unrun", func(conversation *session.Session) {
			appendContractCancellation(t, conversation, nil)
		}),
		observeConsumedWork(t, "replacement-stays-pending", func(conversation *session.Session) {
			appendContractCancellation(
				t,
				conversation,
				[]llm.UserMessage{
					contractMessage(t, "replacement"),
				},
			)
		}),
		observeConsumedWork(t, "later-turn-absorbs-drop", func(conversation *session.Session) {
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
	buildEvents func(*session.Session),
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
	conversation *session.Session,
	turnNumber int64,
	ending session.TurnEndReason,
) {
	t.Helper()
	appendContractTurnStart(t, conversation, turnNumber)
	appendContractClaim(t, conversation)
	if _, appendErr := session.Append(
		conversation,
		session.StepStarted,
		session.StepPosition{
			Turn: turnNumber,
			Step: 1,
		},
	); appendErr != nil {
		t.Fatal(appendErr)
	}
	appendContractTurnEnd(t, conversation, turnNumber, ending)
}

func appendContractTurnStart(
	t *testing.T,
	conversation *session.Session,
	turnNumber int64,
) {
	t.Helper()
	if _, appendErr := session.Append(
		conversation,
		session.TurnStarted,
		session.TurnStart{
			Turn: turnNumber,
		},
	); appendErr != nil {
		t.Fatal(appendErr)
	}
}

func appendContractTurnEnd(
	t *testing.T,
	conversation *session.Session,
	turnNumber int64,
	ending session.TurnEndReason,
) {
	t.Helper()
	if _, appendErr := session.Append(
		conversation,
		session.TurnEnded,
		session.TurnEnd{
			Turn:   turnNumber,
			Reason: ending,
		},
	); appendErr != nil {
		t.Fatal(appendErr)
	}
}

func appendContractClaim(
	t *testing.T,
	conversation *session.Session,
) {
	t.Helper()
	removedCount := 1
	if _, appendErr := session.Append(
		conversation,
		agentcore.InboxSpliced,
		agentcore.InboxSplice{
			Target:       agentcore.NextTurn,
			Start:        0,
			RemovedCount: &removedCount,
			Inserted:     []llm.UserMessage{},
		},
	); appendErr != nil {
		t.Fatal(appendErr)
	}
}

func appendContractCancellation(
	t *testing.T,
	conversation *session.Session,
	replacementMessages []llm.UserMessage,
) {
	t.Helper()
	if replacementMessages == nil {
		replacementMessages = []llm.UserMessage{}
	}
	removedCount := 1
	if _, appendErr := session.Append(
		conversation,
		agentcore.InboxSpliced,
		agentcore.InboxSplice{
			Target:       agentcore.NextTurn,
			Start:        0,
			RemovedCount: &removedCount,
			Inserted:     replacementMessages,
			Outcome:      agentcore.InboxCanceled,
		},
	); appendErr != nil {
		t.Fatal(appendErr)
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
