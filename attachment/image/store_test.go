package image

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/gorenx/goren/attachment"
)

type memoryObjects struct {
	mu        sync.Mutex
	data      map[attachment.AttachmentID][]byte
	published []attachment.AttachmentID
	cache     map[string][]byte
}

func (storage *memoryObjects) Publish(_ context.Context, id attachment.AttachmentID, data []byte) error {
	storage.mu.Lock()
	defer storage.mu.Unlock()
	if storage.data == nil {
		storage.data = make(map[attachment.AttachmentID][]byte)
	}
	storage.data[id] = slices.Clone(data)
	storage.published = append(storage.published, id)
	return nil
}

func (storage *memoryObjects) Read(_ context.Context, id attachment.AttachmentID, _ int64) ([]byte, error) {
	storage.mu.Lock()
	defer storage.mu.Unlock()
	return slices.Clone(storage.data[id]), nil
}

func (*memoryObjects) HostPath(attachment.AttachmentID) (string, error) { return "", nil }

func TestImageBatchPreparationPrecedesAnyPublication(t *testing.T) {
	backend := &scriptedEngine{
		source: Metadata{
			MediaType: ImagePNG,
			Width:     2,
			Height:    3,
			Depth:     "uchar",
			Space:     "srgb",
		},
	}
	storage := &memoryObjects{}
	service, err := NewStore(DefaultConfig(), backend, storage)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { service.Close(context.Background()) })
	input := ImageInput{
		MediaType: ImagePNG,
		Data:      []byte("source"),
		Name:      `C:\Users\private\photo.png`,
	}
	invalid := input
	invalid.MediaType = ImageJPEG
	_, err = service.SaveImages(context.Background(), []ImageInput{input, invalid})
	var failure *attachment.Error
	if !errors.As(err, &failure) || failure.Code != ImageTypeMismatch || len(storage.published) != 0 {
		t.Fatalf("invalid batch: err %v, published %v", err, storage.published)
	}
	refs, err := service.SaveImages(context.Background(), []ImageInput{input, input})
	if err != nil || len(refs) != 2 || refs[0].AttachmentID != refs[1].AttachmentID || refs[0].Name != "photo.png" {
		t.Fatalf("saved batch: %+v, %v", refs, err)
	}
	if refs[0].OriginalDimensions != nil || refs[0].Bytes != 6 {
		t.Fatalf("passthrough reference: %+v", refs[0])
	}
	got, err := service.ReadImage(context.Background(), refs[0])
	if err != nil || string(got.Data) != "source" {
		t.Fatalf("read: %+v, %v", got, err)
	}
	wrong := refs[0]
	wrong.Width++
	_, err = service.ReadImage(context.Background(), wrong)
	if !errors.As(err, &failure) || failure.Code != attachment.AttachmentCorrupt {
		t.Fatalf("wrong metadata read: %v", err)
	}
}

type blockingEngine struct {
	scriptedEngine
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (backend *blockingEngine) Decode(context.Context, []byte) error {
	backend.once.Do(func() { close(backend.entered) })
	<-backend.release
	return nil
}

func TestStoreCloseWaitsForUninterruptibleDecode(t *testing.T) {
	backend := &blockingEngine{
		scriptedEngine: scriptedEngine{
			source: Metadata{
				MediaType: ImagePNG,
				Width:     2,
				Height:    3,
				Depth:     "uchar",
				Space:     "srgb",
			},
		},
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	storage := &memoryObjects{}
	service, err := NewStore(DefaultConfig(), backend, storage)
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		_, err := service.SaveImage(context.Background(), ImageInput{
			MediaType: ImagePNG,
			Data:      []byte("source"),
		})
		result <- err
	}()
	select {
	case <-backend.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("decode did not start")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := service.Close(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("close returned before native decode settled: %v", err)
	}
	if _, err := service.SaveImages(context.Background(), nil); err == nil {
		t.Fatal("close reopened admission")
	}
	close(backend.release)
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("save on close: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("save did not settle after native return")
	}
	if err := service.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(storage.published) != 0 {
		t.Fatal("canceled work published an object")
	}
}

func (storage *memoryObjects) ReadCache(ctx context.Context, key string, maxBytes int64) ([]byte, error) {
	storage.mu.Lock()
	defer storage.mu.Unlock()
	data := storage.cache[key]
	if int64(len(data)) > maxBytes {
		return nil, errors.New("cache too large")
	}
	return slices.Clone(data), ctx.Err()
}
func (storage *memoryObjects) WriteCache(ctx context.Context, key string, data []byte) error {
	storage.mu.Lock()
	defer storage.mu.Unlock()
	if storage.cache == nil {
		storage.cache = make(map[string][]byte)
	}
	storage.cache[key] = slices.Clone(data)
	return ctx.Err()
}
