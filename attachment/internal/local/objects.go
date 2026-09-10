// Package local implements private immutable attachment object storage.
package local

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"

	"github.com/gorenx/goren/attachment"
)

var objectIdentity = regexp.MustCompile(`^sha256:([a-f0-9]{64})$`)

// Objects stores normalized bytes without owning image or Session decisions.
// Its root is fixed and shared safely by concurrent callers.
type Objects struct {
	root string
}

// NewObjects selects an absolute owner-private attachment root. No directory is
// created until publication; constructing a reader does not modify storage.
func NewObjects(root string) (*Objects, error) {
	if !filepath.IsAbs(root) {
		return nil, errors.New("attachment: object root must be absolute")
	}
	return &Objects{
		root: filepath.Clean(root),
	}, nil
}

// HostPath returns a provider-owned path, not authorization or a tool-world path.
func (storage *Objects) HostPath(id attachment.AttachmentID) (string, error) {
	match := objectIdentity.FindStringSubmatch(string(id))
	if match == nil {
		return "", &attachment.Error{
			Code:    attachment.InvalidAttachmentRef,
			Message: "Attachment reference is invalid.",
		}
	}
	return filepath.Join(storage.root, "objects", match[1][:2], match[1]), nil
}

// Read verifies the complete object before returning bytes. File content is
// bounded by the recorded encoded length, not by a potentially changed stat size.
func (storage *Objects) Read(ctx context.Context, id attachment.AttachmentID, expectedBytes int64) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	path, err := storage.HostPath(id)
	if err != nil {
		return nil, err
	}
	if expectedBytes < 0 || expectedBytes > 1<<53-1 {
		return nil, &attachment.Error{
			Code:    attachment.InvalidAttachmentRef,
			Message: "Attachment length is invalid.",
		}
	}
	handle, err := openObject(path)
	if err != nil {
		return nil, readFailure(err)
	}
	defer handle.Close()
	var data []byte
	buffer := make([]byte, 64<<10)
	remaining := expectedBytes + 1
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
	if int64(len(data)) != expectedBytes || contentID(data) != id {
		return nil, corruptObject()
	}
	return data, nil
}

func contentID(data []byte) attachment.AttachmentID {
	digest := sha256.Sum256(data)
	return attachment.AttachmentID("sha256:" + hex.EncodeToString(digest[:]))
}

func openObject(path string) (*os.File, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, corruptObject()
	}
	handle, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	opened, err := handle.Stat()
	if err != nil || !os.SameFile(info, opened) {
		handle.Close()
		return nil, corruptObject()
	}
	return handle, nil
}

func corruptObject() error {
	return &attachment.Error{
		Code:    attachment.AttachmentCorrupt,
		Message: "Stored attachment failed integrity verification.",
	}
}

func readFailure(cause error) error {
	var failure *attachment.Error
	if errors.As(cause, &failure) {
		return cause
	}
	code := attachment.AttachmentReadFailed
	message := "Unable to read image attachment."
	if errors.Is(cause, os.ErrNotExist) {
		code = attachment.AttachmentNotFound
		message = "Attachment object is missing."
	}
	return &attachment.Error{
		Code:    code,
		Message: message,
		Cause:   cause,
	}
}
