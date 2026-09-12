package server

import (
	"context"
	"errors"
	"net"
	"os"
	"runtime"
	"sync"

	"github.com/gorenx/goren/apiproxy/directorypicker"
	"github.com/ncruces/zenity"
)

// directoryPicker owns one native window and its cancellation/close boundary.
type directoryPicker struct {
	mu        sync.Mutex
	available bool
	closed    bool
	cancel    context.CancelFunc
	done      chan struct{}
	choose    func(context.Context) (string, error)
}

func newDirectoryPicker(address string) *directoryPicker {
	host, _, err := net.SplitHostPort(address)
	local := host == "localhost" || net.ParseIP(host).IsLoopback()
	available := err == nil &&
		local &&
		os.Getenv("SSH_CONNECTION") == "" &&
		os.Getenv("SSH_CLIENT") == "" &&
		os.Getenv("SSH_TTY") == ""
	switch runtime.GOOS {
	case "darwin", "windows":
	case "linux":
		available = available &&
			(os.Getenv("DISPLAY") != "" || os.Getenv("WAYLAND_DISPLAY") != "") &&
			zenity.IsAvailable()
	default:
		available = false
	}
	return &directoryPicker{
		available: available,
		choose:    chooseDirectory,
	}
}

func chooseDirectory(ctx context.Context) (string, error) {
	return zenity.SelectFile(zenity.Directory(), zenity.Title("Goren — Select Workspace Directory"), zenity.Context(ctx))
}

func (owner *directoryPicker) Pick(ctx context.Context) (*string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	owner.mu.Lock()
	if !owner.available ||
		owner.closed ||
		owner.done != nil {
		owner.mu.Unlock()
		return nil, directorypicker.ErrUnavailable
	}
	requestContext, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	owner.cancel = cancel
	owner.done = done
	owner.mu.Unlock()
	defer func() {
		cancel()
		owner.mu.Lock()
		owner.cancel = nil
		owner.done = nil
		close(done)
		owner.mu.Unlock()
	}()
	path, err := owner.choose(requestContext)
	if requestContext.Err() != nil {
		return nil, requestContext.Err()
	}
	if errors.Is(err, zenity.ErrCanceled) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &path, nil
}

func (owner *directoryPicker) Close(ctx context.Context) error {
	owner.mu.Lock()
	owner.closed = true
	cancel, done := owner.cancel, owner.done
	owner.mu.Unlock()
	if cancel == nil {
		return nil
	}
	cancel()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
