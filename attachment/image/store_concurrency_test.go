package image

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type pausedEncoder struct {
	scriptedEngine
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (backend *pausedEncoder) Encode(ctx context.Context, data []byte, options Encoding) (Image, error) {
	backend.once.Do(func() { close(backend.entered) })
	<-backend.release
	return backend.scriptedEngine.Encode(ctx, data, options)
}

func TestSharedVariantCancellationAndOccurrenceIsolation(t *testing.T) {
	backend := &pausedEncoder{
		scriptedEngine: scriptedEngine{
			source: Metadata{
				MediaType: ImagePNG,
				Width:     400,
				Height:    300,
				Depth:     "uchar",
				Space:     "srgb",
			},
			lengths: map[int]int{85: 8},
		},
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	service, err := NewStore(DefaultConfig(), backend, &memoryObjects{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { service.Close(context.Background()) })
	ref, err := service.SaveImage(context.Background(), ImageInput{
		MediaType: ImagePNG,
		Data:      []byte("source"),
	})
	if err != nil {
		t.Fatal(err)
	}
	policy := ImageRequestPolicy{
		MaxPixels: 6400,
		MaxBytes:  10,
	}
	type response struct {
		value Variant
		err   error
	}
	results := []chan response{make(chan response, 1), make(chan response, 1), make(chan response, 1)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	for index := range results {
		requestContext := context.Background()
		if index == 0 {
			requestContext = ctx
		}
		occurrence := ref.Clone()
		occurrence.Name = []string{"canceled.png", "second.png", "third.png"}[index]
		go func() {
			value, err := service.ReadImageRequest(requestContext, occurrence, policy)
			results[index] <- response{
				value: value,
				err:   err,
			}
		}()
	}
	deadline := time.After(3 * time.Second)
	for {
		service.mu.Lock()
		joined := false
		for _, work := range service.pending {
			joined = work.waiters == 3
		}
		service.mu.Unlock()
		if joined {
			break
		}
		select {
		case <-deadline:
			close(backend.release)
			t.Fatal("callers did not join the shared job")
		case <-time.After(time.Millisecond):
		}
	}
	cancel()
	select {
	case canceled := <-results[0]:
		if !errors.Is(canceled.err, context.Canceled) {
			t.Fatalf("independent cancellation: %v", canceled.err)
		}
	case <-time.After(3 * time.Second):
		close(backend.release)
		t.Fatal("canceled waiter did not return")
	}
	close(backend.release)
	second := <-results[1]
	third := <-results[2]
	if second.err != nil || third.err != nil {
		t.Fatalf("live waiters failed: %v, %v", second.err, third.err)
	}
	if second.value.Source.Name != "second.png" || third.value.Source.Name != "third.png" || third.value.Image.Ref.Name != "third.png" {
		t.Fatal("shared variant borrowed another occurrence's display name")
	}
	second.value.Image.Data[0] = 0
	if third.value.Image.Data[0] == 0 {
		t.Fatal("waiters share mutable image bytes")
	}
	if len(backend.attempts) != 1 {
		t.Fatalf("shared transform encoded %d times", len(backend.attempts))
	}
}
