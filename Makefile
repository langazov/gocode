BINARY  := opencode
MAIN    := ./cmd/opencode
GO      ?= go

VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

WASM_DIR  := build/wasm
WASM_OUT  := $(WASM_DIR)/app.wasm
GO_WASM_EXEC := $(shell $(GO) env GOROOT)/misc/wasm/wasm_exec.js

.PHONY: help build run install test cover fmt vet lint wasm wasm-run clean

help: ## Show available targets
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-10s\033[0m %s\n", $$1, $$2}'

build: ## Build the opencode binary
	$(GO) build -ldflags '$(LDFLAGS)' -o $(BINARY) $(MAIN)

run: ## Build and run opencode
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

vet: ## Run go vet
	$(GO) vet ./...

lint: vet ## Run vet (alias for static checks)

wasm: ## Build WebAssembly binary (GOOS=js GOARCH=wasm)
	mkdir -p $(WASM_DIR)
	GOOS=js GOARCH=wasm $(GO) build -ldflags '$(LDFLAGS)' -o $(WASM_OUT) $(MAIN)

wasm-run: wasm ## Build for WebAssembly and serve it in the browser
	cp $(GO_WASM_EXEC) $(WASM_DIR)/wasm_exec.js
	printf '%s\n' \
		'<!DOCTYPE html>' \
		'<html>' \
		'<head><meta charset="utf-8"><title>opencode wasm</title></head>' \
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
	rm -rf $(BINARY) coverage.out $(WASM_DIR)
