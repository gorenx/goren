// Package local composes original-image validation and private disk storage.
package local

import (
	"github.com/gorenx/goren/attachment/image"
	"github.com/gorenx/goren/attachment/internal/decoder"
	objects "github.com/gorenx/goren/attachment/internal/local"
)

// Open constructs a Store rooted at an absolute private directory. No image file
// is opened until admission/read; the caller owns Store.Close.
func Open(root string, settings image.Config) (*image.Store, error) {
	storage, err := objects.NewObjects(root)
	if err != nil {
		return nil, err
	}
	return image.NewOriginalStore(settings, &decoder.Inspector{}, storage)
}
