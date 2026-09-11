DATA_DIR ?= $(CURDIR)
LISTEN ?= 127.0.0.1:3080
VERSION ?= dev
GO ?= go
GOREN_BIN ?= $(CURDIR)/.cache/goren
WEB_BUILD_INPUTS := $(shell find web/src -type f) web/index.html web/package.json web/pnpm-lock.yaml web/tsconfig.json web/vite.config.ts

.PHONY: web-build build run plugin-fmt plugin-build plugin-test plugin-test-race plugin-vet architecture-test diff-check plugin-check

web-build: web/dist/index.html

web/dist/index.html: $(WEB_BUILD_INPUTS)
	pnpm -C web run build

build: web-build
	mkdir -p "$(dir $(GOREN_BIN))"
	$(GO) build -o "$(GOREN_BIN)" ./cmd/goren

run: build
	"$(GOREN_BIN)" --listen "$(LISTEN)" --version "$(VERSION)" --data-dir "$(abspath $(DATA_DIR))"

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
