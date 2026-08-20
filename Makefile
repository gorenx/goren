DATA_DIR ?= $(CURDIR)
LISTEN ?= 127.0.0.1:3080
VERSION ?= dev
GO ?= go
GOREN_TMPDIR ?= $(if $(TMPDIR),$(TMPDIR),/tmp)
GOREN_GO_CACHE ?= $(GOREN_TMPDIR)/goren-go-build-cache

export GOCACHE := $(GOREN_GO_CACHE)

.PHONY: web-build run plugin-fmt plugin-build plugin-test plugin-test-race plugin-vet architecture-test diff-check plugin-check

web-build:
	pnpm -C web run build

run: web-build
	$(GO) run ./cmd/goren --listen "$(LISTEN)" --version "$(VERSION)" --data-dir "$(abspath $(DATA_DIR))"

plugin-fmt:
	gofmt -w plugin

plugin-build:
	$(GO) build ./plugin/...

plugin-test:
	$(GO) test ./plugin/...

plugin-test-race:
	$(GO) test -race ./plugin/...

plugin-vet:
	$(GO) vet ./plugin/...

architecture-test:
	$(GO) test ./tests/architecture

diff-check:
	git diff --check

plugin-check: plugin-build plugin-test plugin-test-race plugin-vet architecture-test diff-check
