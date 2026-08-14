package session

import (
	"encoding/json"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"
)

type fixturePayload struct {
	Items []string `json:"items"`
}

type negativeZeroPayload struct{}

func (negativeZeroPayload) MarshalJSON() ([]byte, error) {
	return []byte("-0"), nil
}

var fixtureEventKey = DefineEvent[fixturePayload]("fixture/event")

func TestAppendSnapshotsPayloadAndEventViews(t *testing.T) {
	t.Parallel()
	fixedTime := time.UnixMilli(1_723_700_000_123)
	conversation, err := newWithClock("session-a", CreateOptions{}, func() time.Time { return fixedTime })
	if err != nil {
		t.Fatal(err)
	}
	body := fixturePayload{Items: []string{"first"}}
	committed, err := Append(conversation, fixtureEventKey, body)
	if err != nil {
		t.Fatal(err)
	}
	body.Items[0] = "mutated"
	committed.Data[0] = '['

	snapshot := conversation.Events()
	if len(snapshot) != 1 || snapshot[0].Seq != 0 || snapshot[0].Time != fixedTime.UnixMilli() {
		t.Fatalf("events = %#v", snapshot)
	}
	var decoded fixturePayload
	if err := json.Unmarshal(snapshot[0].Data, &decoded); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decoded.Items, []string{"first"}) {
		t.Fatalf("decoded items = %#v", decoded.Items)
	}
	if _, err := Append(conversation, fixtureEventKey, fixturePayload{Items: []string{"second"}}); err != nil {
		t.Fatal(err)
	}
	if len(snapshot) != 1 || conversation.Seq() != 2 {
		t.Fatalf("old snapshot length = %d, current seq = %d", len(snapshot), conversation.Seq())
	}
}

func TestSeedIsContiguousAndEndsWithLifecycleMarker(t *testing.T) {
	t.Parallel()
	seed := []Event{{Type: "fixture/seed", Seq: 0, Time: 10, Data: json.RawMessage(`{"value":1}`)}}
	conversation, err := New("seeded", CreateOptions{Seed: seed})
	if err != nil {
		t.Fatal(err)
	}
	seed[0].Data[0] = '['
	entries := conversation.Events()
	if conversation.FirstLiveSeq() != 1 || conversation.Seq() != 2 {
		t.Fatalf("first live = %d, seq = %d", conversation.FirstLiveSeq(), conversation.Seq())
	}
	if entries[1].Type != endSeedEventType || string(entries[1].Data) != "{}" {
		t.Fatalf("end-seed entry = %#v", entries[1])
	}
	if string(entries[0].Data) != `{"value":1}` {
		t.Fatalf("seed data = %s", entries[0].Data)
	}

	badSeed := []Event{{Type: "fixture/seed", Seq: 1, Time: 10, Data: json.RawMessage(`{}`)}}
	if _, err := New("bad-seed", CreateOptions{Seed: badSeed}); err == nil || !strings.Contains(err.Error(), "not contiguous") {
		t.Fatalf("non-contiguous seed error = %v", err)
	}
}

func TestSurfaceReplacementIsAtomicAndTracksProvenance(t *testing.T) {
	t.Parallel()
	userKey := defineSurfaceEvent[fixturePayload]("user/message")
	assistantKey := defineSurfaceEvent[fixturePayload]("assistant/message")
	conversation, err := New("surface", CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := AppendSurface(conversation, userKey, fixturePayload{Items: []string{"u"}}, SurfaceIntent{Operation: SurfaceAppend()}); err != nil {
		t.Fatal(err)
	}
	sourceZero := []int64{0}
	if _, err := AppendSurface(conversation, assistantKey, fixturePayload{Items: []string{"a"}}, SurfaceIntent{
		Operation: SurfaceAppend(), SourceEventSeqs: &sourceZero,
	}); err != nil {
		t.Fatal(err)
	}
	missing := []int64{1}
	if _, err := AppendSurface(conversation, assistantKey, fixturePayload{Items: []string{"summary"}}, SurfaceIntent{
		Operation: SurfaceReplace(0, 1), SourceEventSeqs: &missing,
	}); err == nil || !strings.Contains(err.Error(), "missing shadowed seq 0") {
		t.Fatalf("missing provenance error = %v", err)
	}
	if conversation.Seq() != 2 || !reflect.DeepEqual(conversation.Surface().Nodes, []int64{0, 1}) {
		t.Fatalf("failed replacement mutated Session: seq=%d surface=%#v", conversation.Seq(), conversation.Surface())
	}
	allSources := []int64{0, 1}
	if _, err := AppendSurface(conversation, assistantKey, fixturePayload{Items: []string{"summary"}}, SurfaceIntent{
		Operation: SurfaceReplace(0, 1), SourceEventSeqs: &allSources,
	}); err != nil {
		t.Fatal(err)
	}
	view := conversation.Surface()
	if !reflect.DeepEqual(view.Nodes, []int64{2}) || view.ReplaceGeneration != 1 {
		t.Fatalf("surface = %#v", view)
	}
}

func TestAppendRejectsNegativeZeroBeforeCommit(t *testing.T) {
	t.Parallel()
	definition := DefineEvent[negativeZeroPayload]("fixture/negative-zero")
	conversation, err := New("json", CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Append(conversation, definition, negativeZeroPayload{}); err == nil || !strings.Contains(err.Error(), "invalid JSON number") {
		t.Fatalf("negative zero error = %v", err)
	}
	if conversation.Seq() != 0 {
		t.Fatalf("seq = %d after rejected append", conversation.Seq())
	}
	if !math.Signbit(math.Copysign(0, -1)) {
		t.Fatal("test fixture did not construct negative zero")
	}
}
