package loopactor

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gorenx/goren/agentloop"
	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/attachment"
	"github.com/gorenx/goren/attachment/image"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/session"
	sessionactor "github.com/gorenx/goren/session/actor"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/log"
)

// imageProvider observes the real LLM worker boundary; it performs no raster I/O.
type imageProvider struct {
	received chan []agentmessage.Message
}

func (backend *imageProvider) Stream(_ context.Context, options llm.GenerateOptions) (llm.ChunkStream, error) {
	backend.received <- options.Messages
	return llm.NewSliceStream([]llm.StreamChunk{
		llm.BlockStartChunk{
			Index:     0,
			BlockType: "text",
		},
		llm.BlockEndChunk{
			Index: 0,
			Block: agentmessage.NewTextBlock("image received"),
		},
		llm.FinishChunk{
			Reason: llm.StopFinish{},
		},
	})
}

func TestImageReferenceReachesModelAndSurvivesSessionReplay(t *testing.T) {
	for _, mode := range []string{"followup", "steer"} {
		t.Run(mode, func(t *testing.T) {
			ctx, stop := context.WithTimeout(context.Background(), 10*time.Second)
			defer stop()
			system, err := goakt.NewActorSystem("image-admission", goakt.WithLogger(log.DiscardLogger), goakt.WithPubSub())
			if err != nil {
				t.Fatal(err)
			}
			if err := system.Start(ctx); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := system.Stop(context.Background()); err != nil {
					t.Error(err)
				}
			})
			archive, err := session.NewLog("settlement", session.CreateOptions{})
			if err != nil {
				t.Fatal(err)
			}
			subject := &agentloop.ReactLoopAgent{}
			if err := subject.Restore(archive.Header(), archive.Snapshot()); err != nil {
				t.Fatal(err)
			}
			if err := subject.Initialize(); err != nil {
				t.Fatal(err)
			}
			storage, err := goakt.GrainOf[*sessionGrain](ctx, system, "image-session", goakt.WithLongLivedGrain())
			if err != nil {
				t.Fatal(err)
			}
			if _, err := system.AskGrain(ctx, storage, sessionSetup{
				history: archive,
			}, time.Second); err != nil {
				t.Fatal(err)
			}
			backend := &imageProvider{
				received: make(chan []agentmessage.Message, 1),
			}
			pool := modelPool(t, system, backend)
			target, err := goakt.GrainOf[*executionGrain](ctx, system, "image-loop", goakt.WithLongLivedGrain())
			if err != nil {
				t.Fatal(err)
			}
			if _, err := system.AskGrain(ctx, target, executionSetup{
				subject: subject,
				target:  storage,
				router:  pool,
				timeout: time.Second,
			}, time.Second); err != nil {
				t.Fatal(err)
			}
			ref := image.ImageRef{
				AttachmentID: attachment.AttachmentID("sha256:" + strings.Repeat("a", 64)),
				MediaType:    image.ImagePNG,
				Width:        2,
				Height:       3,
				Bytes:        123,
				Name:         "photo.png",
				OriginalDimensions: &image.ImageDimensions{
					Width:  20,
					Height: 30,
				},
			}
			wanted := ref.Clone()
			input, err := agentmessage.NewUserMessage(agentmessage.UserMessageInput{
				Content: []agentmessage.ContentBlock{
					agentmessage.NewTextBlock("describe this"),
					agentmessage.ImageBlock{
						Attachment: ref,
					},
				},
				Source: agentmessage.UserMessageSource{},
			})
			if err != nil {
				t.Fatal(err)
			}
			// Caller metadata cannot mutate the admitted message snapshot.
			ref.OriginalDimensions.Width = 999
			refused, err := system.AskGrain(ctx, target, Followup{
				CreationID: "old-creation",
				Message:    input,
			}, time.Second)
			if err != nil || refused.(Changed).Err == nil {
				t.Fatalf("old creation accepted: %+v, %v", refused, err)
			}
			var command any = Followup{
				CreationID: "test",
				Message:    input,
			}
			if mode == "steer" {
				command = Steer{
					CreationID: "test",
					Message:    input,
				}
			}
			admitted, err := system.AskGrain(ctx, target, command, time.Second)
			if err != nil || admitted.(Changed).Err != nil {
				t.Fatalf("admission: %+v, %v", admitted, err)
			}
			completed, err := system.AskGrain(ctx, target, WhenIdle{
				CreationID: "test",
			}, 5*time.Second)
			if err != nil || completed.(Completion).Err != nil {
				t.Fatalf("execution: %+v, %v", completed, err)
			}
			var messages []agentmessage.Message
			select {
			case messages = <-backend.received:
			case <-ctx.Done():
				t.Fatal("image did not reach LLM worker")
			}
			assertImageMessage(t, messages, wanted)
			for _, entry := range messages {
				for _, block := range entry.ContentValue() {
					if picture, ok := block.(agentmessage.ImageBlock); ok {
						picture.Attachment.OriginalDimensions.Width = 777
					}
				}
			}
			payload, err := system.AskGrain(ctx, storage, sessionactor.Read{}, time.Second)
			if err != nil {
				t.Fatal(err)
			}
			snapshot := payload.(sessionactor.View).Snapshot
			encoded, err := json.Marshal(snapshot.Events)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "session.json")
			if err := os.WriteFile(path, encoded, 0600); err != nil {
				t.Fatal(err)
			}
			encoded, err = os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var events []session.Event
			if err := json.Unmarshal(encoded, &events); err != nil {
				t.Fatal(err)
			}
			replay, err := session.NewLog("settlement", session.CreateOptions{
				Seed: events,
			})
			if err != nil {
				t.Fatal(err)
			}
			restored, err := replay.DeriveMessages()
			if err != nil {
				t.Fatal(err)
			}
			assertImageMessage(t, restored, wanted)
		})
	}
}

func assertImageMessage(t *testing.T, messages []agentmessage.Message, want image.ImageRef) {
	t.Helper()
	count := 0
	for _, message := range messages {
		for _, block := range message.ContentValue() {
			if picture, ok := block.(agentmessage.ImageBlock); ok {
				count++
				if !reflect.DeepEqual(picture.Attachment, want) {
					t.Fatalf("image metadata changed: %+v", picture.Attachment)
				}
			}
		}
	}
	if count != 1 {
		t.Fatalf("image occurrences: got %d, want 1", count)
	}
}
