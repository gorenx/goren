//go:build contract

package contract_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/gorenx/goren/session"
)

type sessionObservation struct {
	Header       session.Header  `json:"header"`
	FirstLiveSeq int64           `json:"firstLiveSeq"`
	Seq          int64           `json:"seq"`
	Events       []session.Event `json:"events"`
	Surface      struct {
		Nodes             []int64 `json:"nodes"`
		ReplaceGeneration uint64  `json:"replaceGeneration"`
	} `json:"surface"`
}

func TestPinnedSourceSessionCoreMatchesGo(t *testing.T) {
	repositoryRoot, sourceRoot := contractPaths(t)
	commandContext, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	sourceOutput, err := runTypeScript(commandContext, sourceRoot,
		filepath.Join(repositoryRoot, "tests", "contract", "typescript", "session-core.ts"),
		sourceRoot, filepath.Join(repositoryRoot, "contracts", "deepseek-harness", "manifest.json"),
	)
	if err != nil {
		t.Fatal(err)
	}

	createdAt := int64(100)
	blank, err := session.New("blank", session.CreateOptions{Metadata: session.Metadata{CreatedAt: &createdAt}})
	if err != nil {
		t.Fatal(err)
	}
	appended, err := session.New("appended", session.CreateOptions{Metadata: session.Metadata{CreatedAt: &createdAt}})
	if err != nil {
		t.Fatal(err)
	}
	fixtureKey := session.DefineEvent[struct {
		Items []string `json:"items"`
	}]("fixture/event")
	if _, err := session.Append(appended, fixtureKey, struct {
		Items []string `json:"items"`
	}{Items: []string{"value"}}); err != nil {
		t.Fatal(err)
	}
	appendOperation := session.SurfaceAppend()
	replaceOperation := session.SurfaceReplace(0, 0)
	provenance := []int64{0}
	seed := []session.Event{
		{
			Type: "user/message", Seq: 0, Time: 1,
			Data:      json.RawMessage(`{"id":"message-1","role":"user","content":[{"type":"text","text":"original"}],"source":{"kind":"user"}}`),
			SurfaceOp: &appendOperation,
		},
		{
			Type: "user/message", Seq: 1, Time: 2,
			Data:            json.RawMessage(`{"id":"message-2","role":"user","content":[{"type":"text","text":"summary"}],"source":{"kind":"plugin","plugin":"fixture"}}`),
			SourceEventSeqs: &provenance, SurfaceOp: &replaceOperation,
		},
	}
	seeded, err := session.New("seeded", session.CreateOptions{
		Seed: seed, Metadata: session.Metadata{CreatedAt: &createdAt},
	})
	if err != nil {
		t.Fatal(err)
	}
	goOutput, err := json.Marshal(map[string]sessionObservation{
		"blank": observeSession(blank), "appended": observeSession(appended), "seeded": observeSession(seeded),
	})
	if err != nil {
		t.Fatal(err)
	}
	assertJSONEqual(t, goOutput, sourceOutput)
}

func observeSession(conversation *session.Session) sessionObservation {
	headerSnapshot := conversation.Header()
	headerSnapshot.CreatedAt = 0
	entries := conversation.Events()
	for index := range entries {
		entries[index].Time = 0
	}
	view := conversation.Surface()
	observation := sessionObservation{
		Header: headerSnapshot, FirstLiveSeq: conversation.FirstLiveSeq(), Seq: conversation.Seq(), Events: entries,
	}
	observation.Surface.Nodes = view.Nodes
	observation.Surface.ReplaceGeneration = view.ReplaceGeneration
	return observation
}
