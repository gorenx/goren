//go:build contract

package contract_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	agentcore "github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/session"
)

type inboxNotificationObservation struct {
	Kind string        `json:"kind"`
	ID   llm.MessageID `json:"id"`
	Turn *int64        `json:"turn,omitempty"`
}

type inboxNotificationsRecorder struct {
	entries []inboxNotificationObservation
}

func (recorder *inboxNotificationsRecorder) Inserted(message llm.UserMessage) {
	recorder.entries = append(recorder.entries, inboxNotificationObservation{Kind: "inserted", ID: message.StableID()})
}

func (recorder *inboxNotificationsRecorder) Discarded(message llm.UserMessage) {
	recorder.entries = append(recorder.entries, inboxNotificationObservation{Kind: "discarded", ID: message.StableID()})
}

func (recorder *inboxNotificationsRecorder) Claimed(message llm.UserMessage, turn int64) {
	turnSnapshot := turn
	recorder.entries = append(recorder.entries, inboxNotificationObservation{Kind: "claimed", ID: message.StableID(), Turn: &turnSnapshot})
}

type inboxEventObservation struct {
	Type string          `json:"type"`
	Seq  int64           `json:"seq"`
	Data json.RawMessage `json:"data"`
}

type inboxStateObservation struct {
	NextTurn   []llm.MessageID `json:"nextTurn"`
	NextStep   []llm.MessageID `json:"nextStep"`
	HasPending bool            `json:"hasPending"`
}

type inboxContractObservation struct {
	Events        []inboxEventObservation        `json:"events"`
	Notifications []inboxNotificationObservation `json:"notifications"`
	Removed       []llm.MessageID                `json:"removed"`
	Claimed       []llm.MessageID                `json:"claimed"`
	Snapshots     struct {
		AfterReplace inboxStateObservation `json:"afterReplace"`
		AfterSplice  inboxStateObservation `json:"afterSplice"`
		AfterClaim   inboxStateObservation `json:"afterClaim"`
		AfterClear   inboxStateObservation `json:"afterClear"`
	} `json:"snapshots"`
}

func TestPinnedSourceAgentInboxMatchesGo(t *testing.T) {
	repositoryRoot, sourceRoot := contractPaths(t)
	commandContext, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	sourceOutput, err := runTypeScript(commandContext, sourceRoot,
		filepath.Join(repositoryRoot, "tests", "contract", "typescript", "agent-inbox.ts"),
		sourceRoot, filepath.Join(repositoryRoot, "contracts", "deepseek-harness", "manifest.json"),
	)
	if err != nil {
		t.Fatal(err)
	}

	createdAt := int64(100)
	conversation, err := session.New("agent-inbox-contract", session.CreateOptions{Metadata: session.Metadata{CreatedAt: &createdAt}})
	if err != nil {
		t.Fatal(err)
	}
	recorder := &inboxNotificationsRecorder{}
	pending, err := agentcore.NewInbox(conversation, recorder)
	if err != nil {
		t.Fatal(err)
	}
	messages := map[string]llm.UserMessage{
		"firstTurn":   decodeInboxMessage(t, "message-1", "first turn"),
		"laterStep":   decodeInboxMessage(t, "message-2", "later step"),
		"firstStep":   decodeInboxMessage(t, "message-3", "first step"),
		"replacement": decodeInboxMessage(t, "message-4", "replacement"),
		"splicedTurn": decodeInboxMessage(t, "message-5", "spliced turn"),
		"clearedTurn": decodeInboxMessage(t, "message-6", "cleared turn"),
		"clearedStep": decodeInboxMessage(t, "message-7", "cleared step"),
	}
	if err := pending.Append(agentcore.NextTurn, messages["firstTurn"]); err != nil {
		t.Fatal(err)
	}
	if err := pending.Append(agentcore.NextStep, messages["laterStep"]); err != nil {
		t.Fatal(err)
	}
	if err := pending.Prepend(agentcore.NextStep, messages["firstStep"]); err != nil {
		t.Fatal(err)
	}
	if replaced, replaceErr := pending.Replace(messages["laterStep"].StableID(), messages["replacement"]); replaceErr != nil || !replaced {
		t.Fatalf("replace = %v, %v", replaced, replaceErr)
	}
	observation := inboxContractObservation{}
	observation.Snapshots.AfterReplace = observeInboxState(pending)
	removed, err := pending.Splice(agentcore.NextTurn, -1, 3, []llm.UserMessage{messages["splicedTurn"]})
	if err != nil {
		t.Fatal(err)
	}
	observation.Snapshots.AfterSplice = observeInboxState(pending)
	claimedMessages, err := pending.Claim(agentcore.NextTurn, 7)
	if err != nil {
		t.Fatal(err)
	}
	observation.Snapshots.AfterClaim = observeInboxState(pending)
	if err := pending.Append(agentcore.NextTurn, messages["clearedTurn"]); err != nil {
		t.Fatal(err)
	}
	if err := pending.Append(agentcore.NextStep, messages["clearedStep"]); err != nil {
		t.Fatal(err)
	}
	if err := pending.Clear(); err != nil {
		t.Fatal(err)
	}
	observation.Snapshots.AfterClear = observeInboxState(pending)
	observation.Removed = inboxMessageIDs(removed)
	observation.Claimed = inboxMessageIDs(claimedMessages)
	observation.Notifications = recorder.entries
	for _, entry := range conversation.Events() {
		observation.Events = append(observation.Events, inboxEventObservation{Type: entry.Type, Seq: entry.Seq, Data: entry.Data})
	}
	goOutput, err := json.Marshal(observation)
	if err != nil {
		t.Fatal(err)
	}
	assertJSONEqual(t, goOutput, sourceOutput)
}

func decodeInboxMessage(t *testing.T, identifier string, text string) llm.UserMessage {
	t.Helper()
	wireValue := struct {
		ID      llm.MessageID         `json:"id"`
		Role    llm.MessageRole       `json:"role"`
		Content []llm.TextBlock       `json:"content"`
		Source  llm.UserMessageSource `json:"source"`
	}{
		ID: llm.MessageID(identifier), Role: llm.RoleUser,
		Content: []llm.TextBlock{{Type: "text", Text: text}},
		Source:  llm.UserMessageSource{Kind: "user"},
	}
	rawValue, err := json.Marshal(wireValue)
	if err != nil {
		t.Fatal(err)
	}
	message, err := llm.DecodeUserMessage(rawValue)
	if err != nil {
		t.Fatal(err)
	}
	return message
}

func observeInboxState(pending *agentcore.Inbox) inboxStateObservation {
	return inboxStateObservation{
		NextTurn: inboxMessageIDs(pending.NextTurn()), NextStep: inboxMessageIDs(pending.NextStep()), HasPending: pending.HasPending(),
	}
}

func inboxMessageIDs(messages []llm.UserMessage) []llm.MessageID {
	identifiers := make([]llm.MessageID, len(messages))
	for index, message := range messages {
		identifiers[index] = message.StableID()
	}
	return identifiers
}
