DATA_DIR ?= $(CURDIR)
LISTEN ?= 127.0.0.1:3080
VERSION ?= dev

.PHONY: web-build run

web-build:
	pnpm -C web run build

run: web-build
	go run ./cmd/goren --listen "$(LISTEN)" --version "$(VERSION)" --data-dir "$(abspath $(DATA_DIR))"
