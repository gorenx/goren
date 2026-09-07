package cacheactor_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/session/persistence"
	projectionactor "github.com/gorenx/goren/session/projection/actor"
	cacheactor "github.com/gorenx/goren/session/projectioncache/actor"
	goakt "github.com/tochemey/goakt/v4/actor"
)

type history struct {
	mu             sync.Mutex
	starts         []int64
	header         session.Header
	total          int64
	changeRevision bool
	fail           bool
}

func (source *history) ReadEventsFrom(_ context.Context, _ session.SessionID, cursor persistence.EventContinuation) (persistence.EventSegment, error) {
	source.mu.Lock()
	defer source.mu.Unlock()
	source.starts = append(source.starts, cursor.FromSeq)
	if source.fail {
		return persistence.EventSegment{}, errors.New("read failed")
	}
	end := min(source.total, cursor.FromSeq+cursor.Limit)
	page := persistence.EventSegment{
		Header:   source.header,
		Revision: "stable",
		HasMore:  end < source.total,
	}
	if source.changeRevision && cursor.FromSeq > 0 {
		page.Revision = "changed"
	}
	for seq := cursor.FromSeq; seq < end; seq++ {
		page.Events = append(page.Events, session.Event{
			Seq:  seq,
			Type: "fixture",
		})
	}
	return page, nil
}
func (*history) ReadEventsBefore(context.Context, session.SessionID, persistence.EventPage) (persistence.EventWindow, error) {
	return persistence.EventWindow{}, errors.New("unexpected reverse read")
}

func TestColdSnapshotPagesDurableLogAndRejectsRevisionChanges(t *testing.T) {
	for _, scenario := range []struct {
		name          string
		units         bool
		changed       bool
		fail          bool
		initial       bool
		wrongIdentity bool
		wantErr       bool
	}{
		{
			name:  "multiple pages",
			units: true,
		},
		{
			name: "no Units still reads durable end",
		},
		{
			name:    "changed revision rejected",
			changed: true,
			wantErr: true,
		},
		{
			name:    "read failure returned",
			fail:    true,
			wantErr: true,
		},
		{
			name:    "checkpoint skips prefix",
			units:   true,
			initial: true,
		},
		{
			name:          "wrong checkpoint log restarts from zero",
			units:         true,
			initial:       true,
			wrongIdentity: true,
		},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			system, driver, replies := systemForTest(t)
			backend := &storage{
				entered: make(chan stored, 8),
			}
			identity := openCache(t, system, "cold", backend)
			definitions, err := goakt.GrainOf[*projectionactor.Grain](context.Background(), system, "definitions", goakt.WithLongLivedGrain())
			if err != nil {
				t.Fatal(err)
			}
			if scenario.units {
				result, err := system.AskGrain(context.Background(), definitions, projectionactor.Register{
					Unit: valueUnit{
						version: 1,
					},
				}, time.Second)
				if err != nil {
					t.Fatal(err)
				}
				if failure := result.(projectionactor.Registered).Err; failure != nil {
					t.Fatal(failure)
				}
			}
			if scenario.initial {
				send(t, driver, "seed", identity, checkpoint("cold", 599))
				if result := receive(t, replies).value.(cacheactor.Completion); result.Err != nil {
					t.Fatal(result.Err)
				}
			}
			source := &history{
				header:         checkpoint("cold", 0).Header,
				total:          1100,
				changeRevision: scenario.changed,
				fail:           scenario.fail,
			}
			if scenario.wrongIdentity {
				source.header.CreatedAt++
			}
			send(t, driver, "cold", identity, cacheactor.ColdSnapshot{
				Storage:    source,
				Projection: definitions,
			})
			result := receive(t, replies).value.(cacheactor.ColdView)
			if scenario.wantErr {
				if result.Err == nil || result.Value != nil {
					t.Fatalf("failure = %#v", result)
				}
				return
			}
			if result.Err != nil || result.Value == nil {
				t.Fatalf("read = %#v", result)
			}
			if result.Value.Snapshot.AsOfSeq != 1099 {
				t.Fatalf("cut = %d", result.Value.Snapshot.AsOfSeq)
			}
			if scenario.units && result.Value.Checkpoint["value"].Seq != 1099 {
				t.Fatal("checkpoint did not reach durable end")
			}
			source.mu.Lock()
			starts := fmt.Sprint(source.starts)
			source.mu.Unlock()
			want := "[0 512 1024]"
			if scenario.initial {
				want = "[599]"
			}
			if scenario.wrongIdentity {
				want = "[599 0 512 1024]"
			}
			if starts != want {
				t.Fatalf("pages = %s, want %s", starts, want)
			}
		})
	}
}
