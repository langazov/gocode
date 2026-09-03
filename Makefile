BINARY  := gocode
MAIN    := ./cmd/gocode
GO      ?= go

VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
# The version and channel live in internal/installation, not package main.
# `-X main.version` named a symbol that does not exist, so every build — the
# release target included — reported "local".
VERSION_PKG := github.com/langazov/gocode-go/internal/installation
LDFLAGS := -s -w -X $(VERSION_PKG).Version=$(VERSION)
RELEASE_DIR := dist
RELEASE_LDFLAGS := $(LDFLAGS) -X $(VERSION_PKG).Channel=release

CGO_ENABLED ?= 0

WASM_DIR  := build/wasm
WASM_OUT  := $(WASM_DIR)/app.wasm
GO_WASM_EXEC := $(shell $(GO) env GOROOT)/misc/wasm/wasm_exec.js

.PHONY: help build release run install test cover fmt fmt-check vet lint check wasm wasm-run clean

help: ## Show available targets
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-10s\033[0m %s\n", $$1, $$2}'

build: ## Build the gocode binary
	$(GO) build -ldflags '$(LDFLAGS)' -o $(BINARY) $(MAIN)

release: vet test ## Build optimized release binary into dist/
	$(GO) version
	CGO_ENABLED=$(CGO_ENABLED) $(GO) build -trimpath -buildmode=exe \
		-ldflags '$(RELEASE_LDFLAGS)' \
		-o $(RELEASE_DIR)/$(BINARY) $(MAIN)
	@echo "Release binary: $(RELEASE_DIR)/$(BINARY)"

run: ## Build and run gocode
	$(GO) run $(MAIN)

install: ## Install binary to $GOPATH/bin
	$(GO) install -ldflags '$(LDFLAGS)' $(MAIN)

test: ## Run all tests
	$(GO) test ./...

cover: ## Run tests with coverage report
	$(GO) test -coverprofile=coverage.out ./...
	$(GO) tool cover -func=coverage.out

fmt: ## Format code
	$(GO) fmt ./...

fmt-check: ## Fail if any file is not gofmt-clean (what CI runs)
	@unformatted=$$(gofmt -l .); \
	if [ -n "$$unformatted" ]; then \
		echo "not gofmt-clean:"; echo "$$unformatted"; exit 1; \
	fi

vet: ## Run go vet
	$(GO) vet ./...

lint: vet ## Run vet (alias for static checks)

check: fmt-check vet test ## Everything CI runs: format check, vet, tests

wasm: ## Build WebAssembly binary (GOOS=js GOARCH=wasm)
	mkdir -p $(WASM_DIR)
	GOOS=js GOARCH=wasm $(GO) build -ldflags '$(LDFLAGS)' -o $(WASM_OUT) $(MAIN)

wasm-run: wasm ## Build for WebAssembly and serve it in the browser
	cp $(GO_WASM_EXEC) $(WASM_DIR)/wasm_exec.js
	printf '%s\n' \
		'<!DOCTYPE html>' \
		'<html>' \
		'<head><meta charset="utf-8"><title>gocode wasm</title></head>' \
		'<body>' \
		'<script src="wasm_exec.js"></script>' \
		'<script>' \
		'const go = new Go();' \
		'WebAssembly.instantiateStreaming(fetch("app.wasm"), go.importObject)' \
		'	.then((result) => go.run(result.instance));' \
		'</script>' \
		'</body>' \
		'</html>' \
		> $(WASM_DIR)/index.html
	@echo "Serving $(WASM_DIR) at http://localhost:8080"
	@$(GO) run tools/wasmserve.go $(WASM_DIR)

clean: ## Remove build artifacts
	rm -rf $(BINARY) coverage.out $(RELEASE_DIR) $(WASM_DIR)
