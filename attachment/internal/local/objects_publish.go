package local

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"

	"github.com/gorenx/goren/attachment"
)

// Publish stages and fsyncs bytes before linking an immutable content-addressed
// name into place. Failure after linking may leave an unreferenced shared object.
func (storage *Objects) Publish(ctx context.Context, id attachment.AttachmentID, data []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	path, err := storage.HostPath(id)
	if err != nil {
		return err
	}
	if contentID(data) != id {
		return corruptObject()
	}
	directory := filepath.Dir(path)
	if err := ensureObjectDirectory(directory, storage.root); err != nil {
		return writeFailure(err)
	}
	handle, err := os.CreateTemp(directory, ".image-*.tmp")
	if err != nil {
		return writeFailure(err)
	}
	temporary := handle.Name()
	defer os.Remove(temporary)
	defer handle.Close()
	for start := 0; start < len(data); {
		if err := ctx.Err(); err != nil {
			return err
		}
		end := min(start+(64<<10), len(data))
		count, err := handle.Write(data[start:end])
		if err != nil {
			return writeFailure(err)
		}
		start += count
	}
	if err := handle.Sync(); err != nil {
		return writeFailure(err)
	}
	if err := handle.Close(); err != nil {
		return writeFailure(err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.Link(temporary, path); err != nil {
		if !errors.Is(err, os.ErrExist) {
			return writeFailure(err)
		}
		if _, err := storage.Read(ctx, id, int64(len(data))); err != nil {
			return err
		}
	}
	// Remove the staging name before chmod: Windows's read-only attribute is
	// shared by hard links and otherwise prevents deleting the temporary name.
	if err := os.Remove(temporary); err != nil {
		return writeFailure(err)
	}
	if err := os.Chmod(path, 0o400); err != nil {
		return writeFailure(err)
	}
	for current := directory; ; current = filepath.Dir(current) {
		if err := syncDirectory(current); err != nil {
			return writeFailure(err)
		}
		if current == storage.root {
			break
		}
	}
	return ctx.Err()
}

func ensureObjectDirectory(path, boundary string) error {
	if err := createDurableDirectory(path); err != nil {
		return err
	}
	for current := path; ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err != nil {
			return err
		}
		if !info.IsDir() {
			return errors.New("attachment: storage directory is not a directory")
		}
		if err := os.Chmod(current, 0o700); err != nil {
			return err
		}
		if err := syncDirectory(filepath.Dir(current)); err != nil {
			return err
		}
		if current == boundary {
			return nil
		}
	}
}

func createDurableDirectory(path string) error {
	info, err := os.Lstat(path)
	if err == nil {
		if !info.IsDir() {
			return errors.New("attachment: storage path is not a directory")
		}
		return nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	parent := filepath.Dir(path)
	if err := createDurableDirectory(parent); err != nil {
		return err
	}
	if err := os.Mkdir(path, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	return syncDirectory(parent)
}

func syncDirectory(path string) error {
	if runtime.GOOS == "windows" {
		// NTFS directory-entry durability uses filesystem journaling; Go cannot
		// fsync a Windows directory handle through os.File.Sync.
		return nil
	}
	handle, err := os.Open(path)
	if err != nil {
		return err
	}
	defer handle.Close()
	return handle.Sync()
}

func writeFailure(cause error) error {
	return &attachment.Error{
		Code:    attachment.AttachmentWriteFailed,
		Message: "Unable to persist image attachment.",
		Cause:   cause,
	}
}
