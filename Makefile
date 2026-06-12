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

# Config used by the dev-loop targets. Override on the command line, e.g.
#   make rules CONFIG=configs/dezhban.vpn-guard.json
CONFIG ?= configs/dezhban.local.json
MODE   ?= guard

.PHONY: build vet test build-all clean lint \
        run-dry validate rules doctor \
        install-local reinstall uninstall-local panic

build: ## Build for the host platform into ./$(BINARY)
	go build $(LDFLAGS) -o $(BINARY) $(PKG)

vet: ## Static checks
	go vet ./...

test: ## Run all tests
	go test ./...

lint: ## golangci-lint if installed, else gofmt + vet
	@if command -v golangci-lint >/dev/null 2>&1; then \
		golangci-lint run; \
	else \
		echo "golangci-lint not found; running gofmt + go vet"; \
		test -z "$$(gofmt -l .)" || { echo "gofmt needed:"; gofmt -l .; exit 1; }; \
		go vet ./...; \
	fi

# --- dev loop (no root) -----------------------------------------------------

run-dry: ## Build + run the monitor in dry-run (no firewall touch)
	CONFIG=$(CONFIG) sh scripts/dev.sh

validate: ## Load + validate CONFIG without side effects
	go run $(PKG) validate --config $(CONFIG)

rules: ## Print the ruleset for MODE (guard|fullblock|legacy) without applying
	go run $(PKG) print-rules --mode $(MODE) --config $(CONFIG)

doctor: ## Diagnose VPN guard config (add ARGS=--discover on macOS)
	go run $(PKG) doctor --config $(CONFIG) $(ARGS)

# --- service lifecycle (sudo) ----------------------------------------------

install-local: ## Validate, build, install config + service, start it
	CONFIG=$(CONFIG) sh scripts/install-local.sh

reinstall: ## Tear down then install fresh
	CONFIG=$(CONFIG) sh scripts/reinstall.sh

uninstall-local: ## Stop + unregister the service
	sh scripts/uninstall-local.sh

panic: ## Force-remove dezhban's rules (lockout escape hatch)
	sh scripts/panic.sh

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
