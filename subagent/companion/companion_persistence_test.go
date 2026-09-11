package companion_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/gorenx/goren/session"
	sessionactor "github.com/gorenx/goren/session/actor"
	"github.com/gorenx/goren/session/persistence/sqlite"
	sessionregistry "github.com/gorenx/goren/session/registry"
	"github.com/gorenx/goren/subagent/companion"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/log"
	"github.com/tochemey/goakt/v4/reentrancy"
)

func askSession[T any](t *testing.T, system goakt.ActorSystem, target *goakt.GrainIdentity, input any) T {
	t.Helper()
	payload, err := system.AskGrain(t.Context(), target, input, 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	result, valid := payload.(T)
	if !valid {
		t.Fatalf("unexpected reply %T", payload)
	}
	return result
}

func TestCompanionChangesAreSerializedAndRecoverFromSQLite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.sqlite")
	var firstBarrier session.WriteBarrier
	var finalRevision int64
	for _, stage := range []string{"write", "restore"} {
		t.Run(stage, func(t *testing.T) {
			backend, err := sqlite.Open(t.Context(), sqlite.Config{
				Path:        path,
				JournalMode: sqlite.JournalWAL,
			})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := backend.Close(context.Background()); err != nil {
					t.Error(err)
				}
			})
			system, err := goakt.NewActorSystem("companion-history", goakt.WithLogger(log.DiscardLogger))
			if err != nil {
				t.Fatal(err)
			}
			if err := system.Start(t.Context()); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := system.Stop(context.Background()); err != nil {
					t.Error(err)
				}
			})
			directory, err := goakt.GrainOf[*sessionregistry.RegistryGrain](t.Context(), system, sessionregistry.RegistryName,
				goakt.WithLongLivedGrain(),
				goakt.WithGrainReentrancy(reentrancy.New(reentrancy.WithMode(reentrancy.AllowAll))),
			)
			if err != nil {
				t.Fatal(err)
			}
			var request any = sessionregistry.CreateSession{
				SessionID:  "parent",
				CreationID: stage,
				Storage:    backend,
			}
			if stage == "restore" {
				request = sessionregistry.ResumeSession{
					SessionID:  "parent",
					CreationID: stage,
					Storage:    backend,
				}
			}
			initialized := askSession[sessionactor.Initialized](t, system, directory, request)
			if initialized.Err != nil {
				t.Fatal(initialized.Err)
			}
			endpoint := askSession[sessionregistry.Endpoint](t, system, directory, sessionregistry.ResolveSession{
				SessionID:  "parent",
				CreationID: stage,
			})
			if endpoint.Err != nil || endpoint.Target == nil {
				t.Fatal("missing initialized Session", endpoint.Err)
			}
			original := newRecord(t, firstCompanion())
			if stage == "write" {
				created := askSession[sessionactor.Committed](t, system, endpoint.Target, sessionactor.Commit{
					CreationID: stage,
					Plan:       original,
				})
				if created.Err != nil {
					t.Fatal(created.Err)
				}
				firstBarrier = created.Result.Barrier
				results := make(chan sessionactor.Committed, 2)
				for _, removed := range []bool{false, true} {
					next := firstCompanion()
					next.Revision = 2
					next.Enabled = false
					next.Removed = removed
					plan := newRecord(t, next)
					go func() {
						payload, err := system.AskGrain(t.Context(), endpoint.Target, sessionactor.Commit{
							CreationID: stage,
							Plan:       plan,
						}, 3*time.Second)
						if err != nil {
							results <- sessionactor.Committed{
								Err: err,
							}
							return
						}
						results <- payload.(sessionactor.Committed)
					}()
				}
				var accepted, rejected int
				for range 2 {
					result := <-results
					if result.Err == nil {
						accepted++
					} else if errors.Is(result.Err, companion.ErrConflict) {
						rejected++
					} else {
						t.Fatal(result.Err)
					}
				}
				if accepted != 1 || rejected != 1 {
					t.Fatal("concurrent writes did not enforce one management revision")
				}
				view := askSession[sessionactor.View](t, system, endpoint.Target, sessionactor.Read{
					CreationID: stage,
				})
				current, err := companion.CompanionsFromEvents("parent", view.Snapshot.Events)
				if view.Err != nil || err != nil || len(current) != 1 {
					t.Fatal("cannot reconstruct management state", view.Err, err)
				}
				next := current[0]
				if !next.Removed {
					next.Revision++
					next.Removed = true
					changed := askSession[sessionactor.Committed](t, system, endpoint.Target, sessionactor.Commit{
						CreationID: stage,
						Plan:       newRecord(t, next),
					})
					if changed.Err != nil {
						t.Fatal(changed.Err)
					}
				}
				finalRevision = next.Revision
			}
			view := askSession[sessionactor.View](t, system, endpoint.Target, sessionactor.Read{
				CreationID: stage,
			})
			if view.Err != nil {
				t.Fatal(view.Err)
			}
			flushed := askSession[sessionactor.Flushed](t, system, endpoint.Target, sessionactor.Flush{
				CreationID: stage,
				Barrier:    view.Snapshot.Barrier,
			})
			if flushed.Err != nil || flushed.Barrier.NextSeq < view.Snapshot.Barrier.NextSeq {
				t.Fatal("durability not confirmed", flushed.Err)
			}
			retry := askSession[sessionactor.Committed](t, system, endpoint.Target, sessionactor.Commit{
				CreationID: stage,
				Plan:       original,
			})
			if retry.Err != nil || retry.Result.Barrier != firstBarrier {
				t.Fatal("retry lost the original durable intent", retry.Err)
			}
			stored, err := backend.Load(t.Context(), "parent")
			if err != nil || stored == nil {
				t.Fatal("cannot read flushed relationship", err)
			}
			current, err := companion.CompanionsFromEvents("parent", stored.Events)
			if err != nil || len(current) != 1 || current[0].Revision != finalRevision || !current[0].Removed || current[0].Enabled {
				t.Fatal("durable tombstone or revision lost", err)
			}
			if stage == "restore" {
				stale := askSession[sessionactor.Committed](t, system, endpoint.Target, sessionactor.Commit{
					CreationID: "write",
					Plan:       original,
				})
				if stale.Err == nil {
					t.Fatal("old parent lifetime wrote into restored Session")
				}
			}
			closed := askSession[sessionactor.Closed](t, system, directory, sessionregistry.CloseSession{
				SessionID:  "parent",
				CreationID: stage,
			})
			if closed.Err != nil {
				t.Fatal(closed.Err)
			}
		})
	}
}
