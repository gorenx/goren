//go:build contract

package projection

import (
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/gorenx/goren/session"
	sessionprojection "github.com/gorenx/goren/session/projection"
	contractfixture "github.com/gorenx/goren/tests/contract/fixture"
)

type projectionContractObservation struct {
	Name  string          `json:"name"`
	Value json.RawMessage `json:"value"`
}

func assertProjectionContract(
	t *testing.T,
	scriptName string,
	goObservations []projectionContractObservation,
) {
	t.Helper()
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
			scriptName,
		),
		sourceRoot,
		sourceCommit,
	)
	if sourceErr != nil {
		t.Fatal(sourceErr)
	}
	goOutput, encodeErr := json.Marshal(goObservations)
	if encodeErr != nil {
		t.Fatal(encodeErr)
	}
	if !reflect.DeepEqual(
		decodeProjectionContractJSON(t, goOutput),
		decodeProjectionContractJSON(t, sourceOutput),
	) {
		t.Fatalf("Go observations = %s, source observations = %s", goOutput, sourceOutput)
	}
}

func observeProjection(
	t *testing.T,
	name string,
	unit sessionprojection.Unit,
	events []session.Event,
) projectionContractObservation {
	t.Helper()
	state, stateErr := unit.InitialState()
	if stateErr != nil {
		t.Fatal(stateErr)
	}
	for _, committed := range events {
		transition, applyErr := unit.ApplyState(state, committed)
		if applyErr != nil {
			t.Fatal(applyErr)
		}
		state = transition.State
	}
	view, viewErr := unit.ViewState(state)
	if viewErr != nil {
		t.Fatal(viewErr)
	}
	return projectionContractObservation{
		Name:  name,
		Value: view,
	}
}

func projectionContractEvent(
	eventType string,
	sequence int64,
	timestamp int64,
	data json.RawMessage,
) session.Event {
	return session.Event{
		Type: eventType,
		Seq:  sequence,
		Time: timestamp,
		Data: append(json.RawMessage(nil), data...),
	}
}

func decodeProjectionContractJSON(t *testing.T, rawValue []byte) any {
	t.Helper()
	var decoded any
	if decodeErr := json.Unmarshal(rawValue, &decoded); decodeErr != nil {
		t.Fatal(decodeErr)
	}
	return decoded
}
