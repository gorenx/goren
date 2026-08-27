package subagent

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/tools"
)

func TestSnapshotDescriptorNormalizesAndDetachesVariants(t *testing.T) {
	t.Parallel()
	labelValue := "child"
	oneShotData, err := SnapshotDescriptor(OneShotDescriptor{
		Provider: "spawn",
		Label:    &labelValue,
	})
	if err != nil {
		t.Fatal(err)
	}
	labelValue = "mutated"
	oneShotRaw, err := json.Marshal(oneShotData)
	if err != nil {
		t.Fatal(err)
	}
	if string(oneShotRaw) != `{"version":2,"mode":"one-shot","provider":"spawn","label":"child"}` {
		t.Fatalf("one-shot descriptor = %s", oneShotRaw)
	}

	allowValues := []string{}
	continuableData, err := SnapshotDescriptor(ContinuableDescriptor{
		Provider: "fork",
		Label:    "research",
		ToolFilter: &tools.ToolRestriction{
			Allow: allowValues,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	continuableRaw, err := json.Marshal(continuableData)
	if err != nil {
		t.Fatal(err)
	}
	if string(continuableRaw) != `{"version":2,"mode":"continuable","provider":"fork","label":"research","toolFilter":{"allow":[]}}` {
		t.Fatalf("continuable descriptor = %s", continuableRaw)
	}

	boundData, err := SnapshotDescriptor(BoundDescriptor{
		Provider: "spawn",
		Label:    "resident",
	})
	if err != nil {
		t.Fatal(err)
	}
	boundRaw, err := json.Marshal(boundData)
	if err != nil {
		t.Fatal(err)
	}
	if string(boundRaw) != `{"version":2,"mode":"bound","provider":"spawn","label":"resident"}` {
		t.Fatalf("Bound descriptor = %s", boundRaw)
	}
}

func TestFoldDescriptorUsesFirstAuthoritativeEvent(t *testing.T) {
	t.Parallel()
	events := []session.Event{
		{
			Type: DescriptorEventName,
			Seq:  2,
			Data: json.RawMessage(
				`{"version":2,"mode":"one-shot","provider":"spawn","label":"first"}`,
			),
		},
		{
			Type: DescriptorEventName,
			Seq:  3,
			Data: json.RawMessage(
				`{"version":2,"mode":"continuable","provider":"fork","label":"later"}`,
			),
		},
	}
	identity, found, err := FoldDescriptor(events)
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("descriptor not found")
	}
	selected, matches := identity.(OneShotDescriptor)
	if !matches || selected.Label == nil || *selected.Label != "first" {
		t.Fatalf("folded descriptor = %#v", identity)
	}
}

func TestDescriptorDecodeSeparatesUnsupportedAndCorrupt(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name        string
		rawValue    json.RawMessage
		wantFound   bool
		wantMessage string
	}{
		{
			name:      "future version",
			rawValue:  json.RawMessage(`{"version":2.5,"anything":null}`),
			wantFound: false,
		},
		{
			name:        "unknown current field",
			rawValue:    json.RawMessage(`{"version":2,"mode":"one-shot","provider":"spawn","extra":true}`),
			wantMessage: "unknown field",
		},
		{
			name:        "null optional label",
			rawValue:    json.RawMessage(`{"version":2,"mode":"one-shot","provider":"spawn","label":null}`),
			wantMessage: "label must be a string",
		},
		{
			name:        "empty tool restriction",
			rawValue:    json.RawMessage(`{"version":2,"mode":"continuable","provider":"spawn","label":"child","toolFilter":{}}`),
			wantMessage: "must declare allow and/or deny",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			identity, found, err := FoldDescriptor([]session.Event{
				{
					Type: DescriptorEventName,
					Data: testCase.rawValue,
				},
			})
			if testCase.wantMessage != "" {
				if err == nil || !strings.Contains(err.Error(), testCase.wantMessage) {
					t.Fatalf("FoldDescriptor error = %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if found != testCase.wantFound || identity != nil {
				t.Fatalf("FoldDescriptor = %#v, %t", identity, found)
			}
		})
	}
}
