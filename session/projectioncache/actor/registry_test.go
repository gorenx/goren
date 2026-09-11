package cacheactor_test

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	cacheactor "github.com/gorenx/goren/session/projectioncache/actor"
	"github.com/gorenx/goren/session/projectioncache/sqlite"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/log"
)

func TestRegistrySharesInitializationAndClosesWithoutClosingSharedStorage(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	storage, err := sqlite.Open(ctx, sqlite.Config{
		Path: filepath.Join(t.TempDir(), "checkpoints.sqlite"),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer storage.Close(context.Background())
	system, err := goakt.NewActorSystem("cache-registry", goakt.WithLogger(log.DiscardLogger))
	if err != nil {
		t.Fatal(err)
	}
	err = system.Start(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer system.Stop(context.Background())
	registry, err := goakt.GrainOf[*cacheactor.Registry](ctx, system, cacheactor.RegistryName, goakt.WithLongLivedGrain())
	if err != nil {
		t.Fatal(err)
	}
	payload, err := system.AskGrain(ctx, registry, cacheactor.ConfigureRegistry{
		Storage: storage,
	}, time.Second)
	if err != nil || payload.(cacheactor.Completion).Err != nil {
		t.Fatalf("configure: %v %v", payload, err)
	}
	var callers sync.WaitGroup
	for range 16 {
		callers.Go(func() {
			payload, failure := system.AskGrain(ctx, registry, cacheactor.CacheForSession{
				SessionID: "shared",
			}, time.Second)
			if failure != nil {
				t.Error(failure)
				return
			}
			result, valid := payload.(cacheactor.CacheEndpoint)
			if !valid || result.Err != nil || result.Target == nil || result.Target.Name() != "shared.projection-cache" {
				t.Errorf("concurrent acquisition: %+v", payload)
			}
		})
	}
	callers.Wait()
	payload, err = system.AskGrain(ctx, registry, cacheactor.CacheForSession{SessionID: "shared"}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	target := payload.(cacheactor.CacheEndpoint).Target
	payload, err = system.AskGrain(ctx, target, checkpoint("shared", 5), time.Second)
	if err != nil || payload.(cacheactor.Completion).Err != nil {
		t.Fatalf("write: %v %v", payload, err)
	}
	for range 2 {
		payload, err = system.AskGrain(ctx, registry, cacheactor.CloseRegistry{}, time.Second)
		if err != nil || payload.(cacheactor.Completion).Err != nil {
			t.Fatalf("close: %v %v", payload, err)
		}
	}
	payload, err = system.AskGrain(ctx, registry, cacheactor.CacheForSession{SessionID: "late"}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	refused := payload.(cacheactor.CacheEndpoint)
	if refused.Err == nil || refused.Target != nil {
		t.Fatal("closed Registry reopened a cache")
	}
	records, err := storage.LoadAll(ctx)
	if err != nil || records["shared"].Rows["value"].Seq != 5 {
		t.Fatalf("shared storage closed or checkpoint missing: %v %v", records, err)
	}
}
