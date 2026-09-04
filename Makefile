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

# The example process plugin. The binary must land inside its own directory,
# next to gocode-plugin.json: that manifest names the command as
# "./plugin-echo", resolved relative to the plugin directory, which is what
# lets a plugin ship its own executable.
EXAMPLE_PLUGIN_DIR := examples/plugin-echo
EXAMPLE_PLUGIN_SRC := ./$(EXAMPLE_PLUGIN_DIR)
EXAMPLE_PLUGIN_OUT := $(EXAMPLE_PLUGIN_DIR)/plugin-echo$(shell $(GO) env GOEXE)

# The RAG process plugin. Same reasoning as the example plugin above: the
# binary must land inside its own directory, next to gocode-plugin.json.
RAG_PLUGIN_DIR := cmd/rag-plugin
RAG_PLUGIN_SRC := ./$(RAG_PLUGIN_DIR)
RAG_PLUGIN_OUT := $(RAG_PLUGIN_DIR)/rag-plugin$(shell $(GO) env GOEXE)

# Where a plugin referred to by bare name is looked up. This must match
# plugin.InstallRoot() in internal/plugin/loader.go, which is pinned by
# TestInstallDir. Note there is no second "gocode" segment: global.Paths.Config
# already ends in the app name.
PLUGIN_ROOT := $(if $(XDG_CONFIG_HOME),$(XDG_CONFIG_HOME),$(HOME)/.config)/gocode/plugin

# install-plugin copies a directory; NAME defaults to its basename, and is what
# gocode.json then refers to.
PLUGIN ?=
NAME   ?= $(notdir $(patsubst %/,%,$(PLUGIN)))

# Copying a plugin does not enable it: it runs only when the config's `plugin`
# array names it. The install targets therefore also register it in the global
# config. Set CONFIGURE=0 to install the files and leave the config alone.
CONFIGURE ?= 1
OPTIONS   ?=
PLUGIN_CONFIG := $(GO) run tools/pluginconfig.go

.PHONY: help build release run install test cover fmt fmt-check vet lint check wasm wasm-run \
        example-plugin install-plugin install-example-plugin uninstall-plugin \
        enable-plugin disable-plugin plugin-root clean \
        rag-plugin install-rag-plugin mdlsp install-mdlsp

help: ## Show available targets
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2}'

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
	# tools/ carries //go:build ignore, so ./... skips it. pluginconfig is run
	# by the install targets rather than by hand, so it is vetted explicitly
	# instead of being left to rot until someone installs a plugin.
	$(GO) vet tools/pluginconfig.go

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

example-plugin: ## Build the example process plugin into examples/plugin-echo/
	$(GO) build -o $(EXAMPLE_PLUGIN_OUT) $(EXAMPLE_PLUGIN_SRC)
	@echo 'Example plugin: $(EXAMPLE_PLUGIN_OUT)'
	@echo 'Enable it with: "plugin": [["$(CURDIR)/$(EXAMPLE_PLUGIN_DIR)", {"banner": "hi"}]]'

plugin-root: ## Print where installed plugins live
	@echo '$(PLUGIN_ROOT)'

install-plugin: ## Install a plugin directory: make install-plugin PLUGIN=./path [NAME=id]
	@if [ -z '$(PLUGIN)' ]; then \
		echo 'usage: make install-plugin PLUGIN=<directory> [NAME=<id>]'; exit 2; \
	fi
	@if [ ! -f '$(PLUGIN)/gocode-plugin.json' ]; then \
		echo 'error: $(PLUGIN)/gocode-plugin.json not found — a plugin directory must declare how it runs'; exit 1; \
	fi
	@rm -rf '$(PLUGIN_ROOT)/$(NAME)'
	@mkdir -p '$(PLUGIN_ROOT)/$(NAME)'
	cp -R '$(PLUGIN)/.' '$(PLUGIN_ROOT)/$(NAME)/'
	@echo 'Installed to $(PLUGIN_ROOT)/$(NAME)'
	@if [ '$(CONFIGURE)' = '1' ]; then $(PLUGIN_CONFIG) -add '$(NAME)' -options '$(OPTIONS)'; \
	else echo 'Enable it with: "plugin": ["$(NAME)"]'; fi

# Builds into the install directory rather than copying the source tree, so
# only the manifest and the binary are installed — not main.go and README.md.
install-example-plugin: ## Build and install the example plugin as "plugin-echo"
	@mkdir -p '$(PLUGIN_ROOT)/plugin-echo'
	$(GO) build -o '$(PLUGIN_ROOT)/plugin-echo/plugin-echo$(shell $(GO) env GOEXE)' $(EXAMPLE_PLUGIN_SRC)
	cp '$(EXAMPLE_PLUGIN_DIR)/gocode-plugin.json' '$(PLUGIN_ROOT)/plugin-echo/'
	@echo 'Installed to $(PLUGIN_ROOT)/plugin-echo'
	@if [ '$(CONFIGURE)' = '1' ]; then $(PLUGIN_CONFIG) -add plugin-echo -options '$(OPTIONS)'; \
	else echo 'Enable it with: "plugin": ["plugin-echo"]'; fi

rag-plugin: ## Build the RAG process plugin into cmd/rag-plugin/
	$(GO) build -o $(RAG_PLUGIN_OUT) $(RAG_PLUGIN_SRC)
	@echo 'RAG plugin: $(RAG_PLUGIN_OUT)'
	@echo 'Enable it with: "plugin": [["$(CURDIR)/$(RAG_PLUGIN_DIR)", {"embeddingProvider": "openai"}]]'

# The markdown language server. A standalone LSP binary: point your editor at
# it (VS Code "go.languageServerFlags"-style config, nvim-lspconfig, ...).
MDLSP_OUT := cmd/mdlsp/mdlsp$(shell $(GO) env GOEXE)

mdlsp: ## Build the markdown LSP server into cmd/mdlsp/
	$(GO) build -o $(MDLSP_OUT) ./cmd/mdlsp
	@echo 'mdlsp: $(MDLSP_OUT)'
	@echo 'Wire your editor to: $(CURDIR)/$(MDLSP_OUT) (runs on stdio)'

# gocode's built-in LSP registry names `mdlsp` for .md files but, like every
# other server, starts it only when it is on PATH. This target puts it there.
install-mdlsp: ## Install the markdown LSP server to $GOPATH/bin
	$(GO) install ./cmd/mdlsp
	@echo 'Installed mdlsp; gocode now runs it on markdown files.'

# Builds into the install directory rather than copying the source tree, so
# only the manifest and the binary are installed — not main.go and README.md.
install-rag-plugin: ## Build and install the RAG plugin as "rag-plugin"
	@mkdir -p '$(PLUGIN_ROOT)/rag-plugin'
	$(GO) build -o '$(PLUGIN_ROOT)/rag-plugin/rag-plugin$(shell $(GO) env GOEXE)' $(RAG_PLUGIN_SRC)
	cp '$(RAG_PLUGIN_DIR)/gocode-plugin.json' '$(PLUGIN_ROOT)/rag-plugin/'
	@echo 'Installed to $(PLUGIN_ROOT)/rag-plugin'
	@if [ '$(CONFIGURE)' = '1' ]; then $(PLUGIN_CONFIG) -add rag-plugin -options '$(OPTIONS)'; \
	else echo 'Enable it with: "plugin": ["rag-plugin"]'; fi

uninstall-plugin: ## Remove an installed plugin: make uninstall-plugin NAME=id
	@if [ -z '$(NAME)' ]; then echo 'usage: make uninstall-plugin NAME=<id>'; exit 2; fi
	@if [ '$(CONFIGURE)' = '1' ]; then $(PLUGIN_CONFIG) -remove '$(NAME)'; fi
	rm -rf '$(PLUGIN_ROOT)/$(NAME)'
	@echo 'Removed $(PLUGIN_ROOT)/$(NAME)'

enable-plugin: ## Add an installed plugin to the global config: NAME=id [OPTIONS='{...}']
	@if [ -z '$(NAME)' ]; then echo "usage: make enable-plugin NAME=<id> [OPTIONS='{\"k\":1}']"; exit 2; fi
	@$(PLUGIN_CONFIG) -add '$(NAME)' -options '$(OPTIONS)'

disable-plugin: ## Remove a plugin from the global config, leaving it installed: NAME=id
	@if [ -z '$(NAME)' ]; then echo 'usage: make disable-plugin NAME=<id>'; exit 2; fi
	@$(PLUGIN_CONFIG) -remove '$(NAME)'

clean: ## Remove build artifacts (installed plugins are left alone)
	rm -rf $(BINARY) coverage.out $(RELEASE_DIR) $(WASM_DIR) $(EXAMPLE_PLUGIN_OUT) $(MDLSP_OUT)
