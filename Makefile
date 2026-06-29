# CoinRollHunter — single Go binary with an embedded Svelte UI.
SHELL := /bin/bash
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: help ui build run test vet check e2e release clean

help: ## list targets
	@grep -hE '^[a-z-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-10s\033[0m %s\n",$$1,$$2}'

ui: ## build the Svelte UI into web/dist
	cd web/app && npm install && npm run build

build: ui ## build the local binary (with embedded UI)
	CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS)' -o coinrollhunter ./cmd/coinrollhunter

run: build ## build then serve on localhost
	./coinrollhunter serve

test: ## run Go tests
	go test ./...

vet: ## go vet
	go vet ./...

check: ## type-check the UI
	cd web/app && npm run check

e2e: ## headless end-to-end QA of the Do tab (builds, serves a throwaway DB)
	./qa/run.sh

release: ## cross-compile + package every platform into dist/
	VERSION=$(VERSION) ./scripts/release.sh

clean: ## remove build output
	rm -rf dist coinrollhunter coinrollhunter.exe web/dist
