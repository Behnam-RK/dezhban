# dezhban build matrix.
#
# Each OS backend is isolated behind build tags (pf/darwin, nftables/linux,
# wf/windows), so every target compiles only its own backend. The version is
# stamped into the binary via -ldflags; nothing else is linked in (macOS still
# shells out to the system `pfctl` at runtime).

BINARY  := dezhban
PKG     := ./cmd/dezhban
DIST    := dist
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -ldflags "-X main.version=$(VERSION)"

# Build matrix: GOOS/GOARCH pairs -> output name.
PLATFORMS := \
	darwin/arm64 \
	darwin/amd64 \
	linux/amd64 \
	linux/arm64 \
	windows/amd64

.PHONY: build vet test build-all clean

build: ## Build for the host platform into ./$(BINARY)
	go build $(LDFLAGS) -o $(BINARY) $(PKG)

vet: ## Static checks
	go vet ./...

test: ## Run all tests
	go test ./...

build-all: ## Cross-compile every platform into ./$(DIST)
	@mkdir -p $(DIST)
	@for p in $(PLATFORMS); do \
		os=$${p%/*}; arch=$${p#*/}; \
		ext=; [ "$$os" = windows ] && ext=.exe; \
		out=$(DIST)/$(BINARY)-$$os-$$arch$$ext; \
		echo "building $$out ($(VERSION))"; \
		GOOS=$$os GOARCH=$$arch CGO_ENABLED=0 go build $(LDFLAGS) -o $$out $(PKG) || exit 1; \
	done
	@echo "done -> $(DIST)/"

clean: ## Remove build artifacts
	rm -rf $(DIST) $(BINARY)
