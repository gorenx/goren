package server

import (
	"path/filepath"

	"github.com/gorenx/goren/attachment/image"
	imagefiles "github.com/gorenx/goren/attachment/image/local"
)

func (owner *Server) openImages() error {
	if owner.settings.Attachments != nil {
		return nil
	}
	root, err := filepath.Abs(filepath.Join(filepath.Dir(owner.settings.SessionDatabase), "attachments"))
	if err != nil {
		return err
	}
	owner.pictures, err = imagefiles.Open(root, image.DefaultConfig())
	if err != nil {
		return err
	}
	owner.settings.Attachments = owner.pictures
	return nil
}
