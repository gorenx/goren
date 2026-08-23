//go:build contract

package subagent

import (
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/gorenx/goren/session"
	contractfixture "github.com/gorenx/goren/tests/contract/fixture"
	"github.com/gorenx/goren/tools"
)

type descriptorContractObservation struct {
	Name  string          `json:"name"`
	Kind  string          `json:"kind"`
	Value json.RawMessage `json:"value,omitempty"`
}

func TestPinnedSourceDescriptorMatchesGo(t *testing.T) {
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
			"subagent-descriptor.ts",
		),
		sourceRoot,
		sourceCommit,
	)
	if sourceErr != nil {
		t.Fatal(sourceErr)
	}
	agentProvider := "deepseek"
	agentModel := "deepseek-chat"
	persona := "reviewer"
	goObservations := []descriptorContractObservation{
		observeDescriptorSnapshot(t, "snapshot-one-shot", OneShotDescriptor{
			Provider: "spawn",
		}),
		observeDescriptorSnapshot(t, "snapshot-continuable", ContinuableDescriptor{
			Provider:      "fork",
			Label:         "review",
			AgentProvider: &agentProvider,
			AgentModel:    &agentModel,
			Persona:       &persona,
			ToolFilter: &tools.ToolRestriction{
				Allow: []string{},
				Deny: []string{
					"write_file",
				},
			},
		}),
		observeDescriptorFold(t, "fold-first-authoritative", []session.Event{
			descriptorContractEvent(1, json.RawMessage(
				`{"version":2,"mode":"one-shot","provider":"spawn","label":"first"}`,
			)),
			descriptorContractEvent(2, json.RawMessage(
				`{"version":2,"mode":"continuable","provider":"fork","label":"later"}`,
			)),
		}),
		observeDescriptorFold(t, "fold-unsupported-first", []session.Event{
			descriptorContractEvent(1, json.RawMessage(`{"version":3}`)),
			descriptorContractEvent(2, json.RawMessage(
				`{"version":2,"mode":"one-shot","provider":"spawn"}`,
			)),
		}),
		observeDescriptorFold(t, "fold-corrupt-current", []session.Event{
			descriptorContractEvent(1, json.RawMessage(
				`{"version":2,"mode":"one-shot","provider":"spawn","extra":true}`,
			)),
		}),
		observeDescriptorFold(t, "fold-empty-tool-filter", []session.Event{
			descriptorContractEvent(1, json.RawMessage(
				`{"version":2,"mode":"continuable","provider":"spawn","label":"child","toolFilter":{}}`,
			)),
		}),
		observeDescriptorFold(t, "fold-null-label", []session.Event{
			descriptorContractEvent(1, json.RawMessage(
				`{"version":2,"mode":"one-shot","provider":"spawn","label":null}`,
			)),
		}),
		observeDescriptorFold(t, "fold-none", nil),
	}
	goOutput, encodeErr := json.Marshal(goObservations)
	if encodeErr != nil {
		t.Fatal(encodeErr)
	}
	if !reflect.DeepEqual(
		decodeDescriptorContractJSON(t, goOutput),
		decodeDescriptorContractJSON(t, sourceOutput),
	) {
		t.Fatalf("Go observations = %s, source observations = %s", goOutput, sourceOutput)
	}
}

func observeDescriptorSnapshot(
	t *testing.T,
	name string,
	identityValue Descriptor,
) descriptorContractObservation {
	t.Helper()
	data, snapshotErr := SnapshotDescriptor(identityValue)
	if snapshotErr != nil {
		return descriptorContractObservation{
			Name: name,
			Kind: "error",
		}
	}
	rawValue, encodeErr := json.Marshal(data)
	if encodeErr != nil {
		t.Fatal(encodeErr)
	}
	return descriptorContractObservation{
		Name:  name,
		Kind:  "value",
		Value: rawValue,
	}
}

func observeDescriptorFold(
	t *testing.T,
	name string,
	events []session.Event,
) descriptorContractObservation {
	t.Helper()
	identityValue, found, foldErr := FoldDescriptor(events)
	if foldErr != nil {
		return descriptorContractObservation{
			Name: name,
			Kind: "error",
		}
	}
	if !found {
		return descriptorContractObservation{
			Name: name,
			Kind: "none",
		}
	}
	rawValue, encodeErr := json.Marshal(identityValue)
	if encodeErr != nil {
		t.Fatal(encodeErr)
	}
	return descriptorContractObservation{
		Name:  name,
		Kind:  "value",
		Value: rawValue,
	}
}

func descriptorContractEvent(
	sequence int64,
	data json.RawMessage,
) session.Event {
	return session.Event{
		Type: DescriptorEventName,
		Seq:  sequence,
		Time: sequence,
		Data: append(json.RawMessage(nil), data...),
	}
}

func decodeDescriptorContractJSON(t *testing.T, rawValue []byte) any {
	t.Helper()
	var decoded any
	if decodeErr := json.Unmarshal(rawValue, &decoded); decodeErr != nil {
		t.Fatal(decodeErr)
	}
	return decoded
}
