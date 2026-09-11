package sessionactor_test

import (
	"context"
	sessionregistry "github.com/gorenx/goren/session/registry"
	"testing"

	"github.com/gorenx/goren/session"
	sessionactor "github.com/gorenx/goren/session/actor"
)

func TestRegistryClosesFailedInitializationAndReleasesIdentity(t *testing.T) {
	for _, scenario := range []string{"missing-log", "invalid-metadata"} {
		t.Run(scenario, func(t *testing.T) {
			backend := newStorage(t)
			system := startSystem(t)
			driver, replies := registryRequester(t, system)
			var failed sessionactor.Initialized
			if scenario == "missing-log" {
				failed = request[sessionactor.Initialized](t, driver, replies, sessionregistry.ResumeSession{
					SessionID:  "conversation",
					CreationID: "failed",
					Storage:    backend,
				})
			} else {
				relative := "relative/path"
				failed = request[sessionactor.Initialized](t, driver, replies, sessionregistry.CreateSession{
					SessionID:  "conversation",
					CreationID: "failed",
					Options: session.CreateOptions{
						Metadata: session.Metadata{
							CWD: &relative,
						},
					},
					Storage: backend,
				})
			}
			if failed.Err == nil {
				t.Fatal("initialization unexpectedly succeeded")
			}
			closed := request[sessionactor.Closed](t, driver, replies, sessionregistry.CloseSession{
				SessionID:  "conversation",
				CreationID: "failed",
			})
			if closed.Err != nil || closed.Barrier.SessionID != "conversation" || closed.Barrier.NextSeq != 0 {
				t.Fatalf("failed initialization cleanup = %#v", closed)
			}
			stored, err := backend.Load(context.Background(), "conversation")
			if err != nil || stored != nil {
				t.Fatalf("cleanup created storage: %#v, %v", stored, err)
			}
			replacement := request[sessionactor.Initialized](t, driver, replies, sessionregistry.CreateSession{
				SessionID:  "conversation",
				CreationID: "replacement",
				Storage:    backend,
			})
			if replacement.Err != nil {
				t.Fatal(replacement.Err)
			}
		})
	}
}

func TestUnauthorizedEmptyCloseCannotSealNewInitialization(t *testing.T) {
	driver, replies := startSession(t, "current")
	rejected := request[sessionactor.Closed](t, driver, replies, sessionactor.Close{
		SessionID:  "conversation",
		CreationID: "stale",
	})
	if rejected.Err == nil {
		t.Fatal("stale close acquired authority")
	}
	initialized := request[sessionactor.Initialized](t, driver, replies, sessionactor.Initialize{
		SessionID:  "conversation",
		CreationID: "current",
		Storage:    newStorage(t),
	})
	if initialized.Err != nil {
		t.Fatal("stale close sealed valid creation", initialized.Err)
	}
}
