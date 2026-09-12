package session_test

import (
	"context"
	"fmt"

	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/session"
)

// A request header is a log entry, not a replacement for the creation Header.
// This example uses the in-memory Log; the Session actor owns notification and
// persistence. Committing directly to a Log does not publish or flush anything.
func ExampleLog_Commit_requestHeader() {
	conversation, err := session.NewLog("conversation", session.CreateOptions{})
	if err != nil {
		panic(err)
	}
	started, err := session.NewEventDraft(session.TurnStarted, session.TurnStart{
		Turn: 1,
	})
	if err != nil {
		panic(err)
	}
	stepStarted, err := session.NewEventDraft(session.StepStarted, session.StepPosition{
		Turn: 1,
		Step: 1,
	})
	if err != nil {
		panic(err)
	}
	parameters, err := session.NewEventDraft(session.RequestHeaderSet, session.RequestHeaderSnapshot{
		Header: session.EpochHeader{
			Config: llm.CallConfig{
				Provider: "example-provider",
				Model:    "example-model",
			},
		},
		Reason: session.RequestHeaderInitial,
	})
	if err != nil {
		panic(err)
	}
	// These are separate commits in the Agent's execution flow.
	for _, draft := range []session.EventDraft{started, stepStarted, parameters} {
		result, err := conversation.Commit(context.Background(), session.Batch(draft))
		if err != nil {
			panic(err)
		}
		for _, entry := range result.Events {
			fmt.Printf("seq=%d type=%s\n", entry.Seq, entry.Type)
		}
	}
	effective, err := session.LatestRequestHeader(conversation.Events())
	if err != nil {
		panic(err)
	}
	fmt.Println("session:", conversation.Header().ID)
	fmt.Println("model:", effective.Config.Model)
	fmt.Println("notification topic:", session.AppendedEventName)
	// Output:
	// seq=0 type=turn/start
	// seq=1 type=step/start
	// seq=2 type=request/header
	// session: conversation
	// model: example-model
	// notification topic: session/event
}
