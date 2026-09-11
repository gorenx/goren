package local

import (
	"bytes"
	"context"
	"crypto/sha256"
	"io"
	"os"
	"path/filepath"
)

// ReadCache bounds the payload and checks its checksum before returning bytes.
func (storage *Objects) ReadCache(ctx context.Context, key string, maxBytes int64) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	path, err := storage.cachePath(key)
	if err != nil {
		return nil, err
	}
	if maxBytes < 0 || maxBytes > 1<<53-1 {
		return nil, corruptObject()
	}
	// Cache names may be atomically replaced between lookup and open. Pin the
	// opened inode rather than comparing it to a preceding path lookup. Root
	// confines resolution even if a cache path is replaced with a symlink.
	directory, err := os.OpenRoot(storage.root)
	if err != nil {
		return nil, readFailure(err)
	}
	defer directory.Close()
	relative, err := filepath.Rel(storage.root, path)
	if err != nil {
		return nil, readFailure(err)
	}
	handle, err := directory.Open(relative)
	if err != nil {
		return nil, readFailure(err)
	}
	defer handle.Close()
	info, err := handle.Stat()
	if err != nil {
		return nil, readFailure(err)
	}
	if !info.Mode().IsRegular() {
		return nil, corruptObject()
	}
	var header [sha256.Size]byte
	if _, err := io.ReadFull(handle, header[:]); err != nil {
		return nil, readFailure(err)
	}
	var data []byte
	buffer := make([]byte, 64<<10)
	remaining := maxBytes + 1
	for remaining > 0 {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		count, readErr := handle.Read(buffer[:min(int64(len(buffer)), remaining)])
		data = append(data, buffer[:count]...)
		remaining -= int64(count)
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return nil, readFailure(readErr)
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	digest := sha256.Sum256(data)
	if int64(len(data)) > maxBytes || !bytes.Equal(header[:], digest[:]) {
		return nil, corruptObject()
	}
	return data, nil
}

// WriteCache stages a checksum and payload before atomically replacing the entry.
// Cache failure does not change immutable attachment objects.
func (storage *Objects) WriteCache(ctx context.Context, key string, data []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	path, err := storage.cachePath(key)
	if err != nil {
		return err
	}
	directory := filepath.Dir(path)
	if err := ensureObjectDirectory(directory, storage.root); err != nil {
		return writeFailure(err)
	}
	handle, err := os.CreateTemp(directory, ".cache-*.tmp")
	if err != nil {
		return writeFailure(err)
	}
	defer os.Remove(handle.Name())
	defer handle.Close()
	digest := sha256.Sum256(data)
	if _, err := handle.Write(digest[:]); err != nil {
		return writeFailure(err)
	}
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
	if err := os.Rename(handle.Name(), path); err != nil {
		return writeFailure(err)
	}
	if err := syncDirectory(directory); err != nil {
		return writeFailure(err)
	}
	return ctx.Err()
}

func (storage *Objects) cachePath(key string) (string, error) {
	match := objectIdentity.FindStringSubmatch(key)
	if match == nil {
		return "", corruptObject()
	}
	return filepath.Join(storage.root, "request-images", match[1][:2], match[1]), nil
}
