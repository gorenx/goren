package local_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"

	"github.com/gorenx/goren/attachment"
	"github.com/gorenx/goren/attachment/internal/local"
)

func identity(data []byte) attachment.AttachmentID {
	return attachment.AttachmentID(fmt.Sprintf("sha256:%x", sha256.Sum256(data)))
}

func TestConcurrentPublicationAndReopen(t *testing.T) {
	root := filepath.Join(t.TempDir(), "attachments", "v1")
	storage, err := local.NewObjects(root)
	if err != nil {
		t.Fatal(err)
	}
	data := bytes.Repeat([]byte("immutable image bytes"), 10000)
	id := identity(data)
	var running sync.WaitGroup
	for range 12 {
		running.Go(func() {
			if err := storage.Publish(context.Background(), id, data); err != nil {
				t.Error(err)
			}
		})
	}
	running.Wait()
	reopened, err := local.NewObjects(root)
	if err != nil {
		t.Fatal(err)
	}
	got, err := reopened.Read(context.Background(), id, int64(len(data)))
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("reopen read: %v, length %d", err, len(got))
	}
	path, err := reopened.HostPath(id)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o400 {
		t.Fatalf("object permissions: %v", info.Mode())
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil || len(entries) != 1 {
		t.Fatalf("publication left temporary objects: %v, %v", entries, err)
	}
}

func TestRejectsCorruptionAndDoesNotReplaceExistingObject(t *testing.T) {
	storage, err := local.NewObjects(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	data := []byte("image data")
	id := identity(data)
	ctx := context.Background()
	if err := storage.Publish(ctx, id, data); err != nil {
		t.Fatal(err)
	}
	for _, length := range []int64{0, int64(len(data) - 1), int64(len(data) + 1)} {
		_, err := storage.Read(ctx, id, length)
		assertCode(t, err, attachment.AttachmentCorrupt)
	}
	path, err := storage.HostPath(id)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	corrupt := []byte("wrong data")
	if err := os.WriteFile(path, corrupt, 0o600); err != nil {
		t.Fatal(err)
	}
	assertCode(t, storage.Publish(ctx, id, data), attachment.AttachmentCorrupt)
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, corrupt) {
		t.Fatal("publication replaced damaged shared object")
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil || len(entries) != 1 {
		t.Fatal("failed publication leaked staging file")
	}
}

func TestCancellationAndInvalidIdentityDoNotPublish(t *testing.T) {
	root := filepath.Join(t.TempDir(), "uncreated")
	storage, err := local.NewObjects(root)
	if err != nil {
		t.Fatal(err)
	}
	data := []byte("bytes")
	id := identity(data)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := storage.Publish(ctx, id, data); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled publish: %v", err)
	}
	if _, err := os.Stat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("canceled publish created root: %v", err)
	}
	for _, invalid := range []attachment.AttachmentID{"../escape", "sha256:abc", attachment.AttachmentID("sha256:" + string(bytes.Repeat([]byte("F"), 64)))} {
		_, err := storage.HostPath(invalid)
		assertCode(t, err, attachment.InvalidAttachmentRef)
	}
	assertCode(t, storage.Publish(context.Background(), id, []byte("different")), attachment.AttachmentCorrupt)
	_, err = storage.Read(context.Background(), id, int64(len(data)))
	assertCode(t, err, attachment.AttachmentNotFound)
}

func assertCode(t *testing.T, err error, want attachment.ErrorCode) {
	t.Helper()
	var failure *attachment.Error
	if !errors.As(err, &failure) || failure.Code != want {
		t.Fatalf("failure = %v; want %s", err, want)
	}
}
