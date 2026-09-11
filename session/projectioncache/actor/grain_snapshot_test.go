package cacheactor_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/session/projection"
	projectionactor "github.com/gorenx/goren/session/projection/actor"
	cacheactor "github.com/gorenx/goren/session/projectioncache/actor"
	goakt "github.com/tochemey/goakt/v4/actor"
)

type valueUnit struct {
	version int64
	fail    bool
}

func (valueUnit) Key() string                            { return "value" }
func (unit valueUnit) StateVersion() int64               { return unit.version }
func (valueUnit) InitialState() (json.RawMessage, error) { return json.RawMessage(`false`), nil }
func (valueUnit) ApplyState(state json.RawMessage, _ session.Event) (projection.Transition, error) {
	return projection.Transition{
		State: state,
	}, nil
}
func (unit valueUnit) ViewState(state json.RawMessage) (json.RawMessage, error) {
	if unit.fail {
		return nil, errors.New("view failed")
	}
	// A view deliberately differs from its checkpoint state.
	return json.RawMessage(`{"saved":` + string(state) + `}`), nil
}

func TestCachedSnapshotUsesCurrentUnitsAndKeepsFailuresDistinctFromMisses(t *testing.T) {
	system, driver, replies := systemForTest(t)
	backend := &storage{
		entered: make(chan stored, 8),
	}
	identity := openCache(t, system, "saved", backend)
	definitions, err := goakt.GrainOf[*projectionactor.Grain](context.Background(), system, "definitions", goakt.WithLongLivedGrain())
	if err != nil {
		t.Fatal(err)
	}
	register := func(unit valueUnit) projection.RegistrationID {
		t.Helper()
		payload, failure := system.AskGrain(context.Background(), definitions, projectionactor.Register{
			Unit: unit,
		}, time.Second)
		if failure != nil {
			t.Fatal(failure)
		}
		result := payload.(projectionactor.Registered)
		if result.Err != nil {
			t.Fatal(result.Err)
		}
		return result.ID
	}
	remove := func(id projection.RegistrationID) {
		t.Helper()
		payload, failure := system.AskGrain(context.Background(), definitions, projectionactor.Remove{
			ID: id,
		}, time.Second)
		if failure != nil {
			t.Fatal(failure)
		}
		if failure := payload.(projectionactor.Completion).Err; failure != nil {
			t.Fatal(failure)
		}
	}
	read := func(header session.Header) cacheactor.Snapshot {
		t.Helper()
		send(t, driver, "snapshot", identity, cacheactor.CachedSnapshot{
			Header:     header,
			Projection: definitions,
		})
		return receive(t, replies).value.(cacheactor.Snapshot)
	}
	header := checkpoint("saved", 5).Header
	if result := read(header); result.Err != nil || result.Value != nil {
		t.Fatalf("absent = %#v", result)
	}
	send(t, driver, "write", identity, checkpoint("saved", 5))
	if result := receive(t, replies).value.(cacheactor.Completion); result.Err != nil {
		t.Fatal(result.Err)
	}
	if result := read(header); result.Err != nil || result.Value != nil {
		t.Fatalf("no Units = %#v", result)
	}
	id := register(valueUnit{
		version: 1,
	})
	result := read(header)
	if result.Err != nil || result.Value == nil || result.Value.AsOfSeq != 5 || string(result.Value.Values["value"]) != `{"saved":true}` {
		t.Fatalf("view = %#v", result)
	}
	result.Value.Values["value"][0] = '!'
	if again := read(header); again.Err != nil || string(again.Value.Values["value"]) != `{"saved":true}` {
		t.Fatalf("aliased view = %#v", again)
	}
	header.CreatedAt++
	if result := read(header); result.Err != nil || result.Value != nil {
		t.Fatalf("wrong log = %#v", result)
	}
	header.CreatedAt--
	remove(id)
	id = register(valueUnit{
		version: 2,
	})
	if result := read(header); result.Err != nil || result.Value != nil {
		t.Fatalf("incompatible version = %#v", result)
	}
	remove(id)
	register(valueUnit{
		version: 1,
		fail:    true,
	})
	if result := read(header); result.Err == nil || result.Value != nil {
		t.Fatalf("hidden view failure = %#v", result)
	}
}
