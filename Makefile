GO          ?= go
OUT_DIR     := ./bin
BINDIR      ?= $(if $(GOBIN),$(GOBIN),$(or $(GOPATH),$(HOME)/go)/bin)
BIN         := lathe-scan
PKG         := github.com/lathe-cli/lathe-scan
# report.json records tool_version; a build must be able to say which one it is.
VERSION     ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS     := -X $(PKG)/internal/scan.version=$(VERSION)

BOLD  := \033[1m
CYAN  := \033[36m
GREEN := \033[32m
RESET := \033[0m

.DEFAULT_GOAL := help

# ── Build ────────────────────────────────────────────────────────────────────

.PHONY: build install

build: ## Build local lathe-scan binary into ./bin/lathe-scan
	@mkdir -p $(OUT_DIR)
	$(GO) build -trimpath -ldflags '$(LDFLAGS)' -o $(OUT_DIR)/$(BIN) .
	@printf '\n$(GREEN)  ✓ built $(CYAN)$(OUT_DIR)/$(BIN)$(RESET) $(VERSION)\n\n'

install: build ## Install local lathe-scan binary into BINDIR
	@mkdir -p $(BINDIR)
	@cp $(OUT_DIR)/$(BIN) $(BINDIR)/$(BIN)
	@printf '\n$(GREEN)  ✓ installed $(CYAN)$(BINDIR)/$(BIN)$(RESET)\n\n'

# ── Quality ──────────────────────────────────────────────────────────────────

.PHONY: check test vet fmt fmt-check lint

check: ## Full quality gate — fmt-check, vet, lint, test
	@printf '\n$(BOLD)[1/4] Checking format$(RESET)\n'
	@$(MAKE) --no-print-directory fmt-check
	@printf '\n$(BOLD)[2/4] Running vet$(RESET)\n'
	$(GO) vet ./...
	@printf '\n$(BOLD)[3/4] Running lint$(RESET)\n'
	@$(MAKE) --no-print-directory lint
	@printf '\n$(BOLD)[4/4] Running tests$(RESET)\n'
	$(GO) test ./...
	@printf '\n$(GREEN)  ✓ All checks passed$(RESET)\n\n'

lint: ## Run golangci-lint
	golangci-lint run ./...

test: ## Run tests
	$(GO) test ./...

vet: ## Run go vet
	$(GO) vet ./...

fmt: ## Format code in place
	$(GO) fmt ./...

fmt-check: ## Fail if any file needs gofmt
	@out=$$(gofmt -l main.go internal bench); \
	if [ -n "$$out" ]; then \
	  printf '$(BOLD)gofmt violations:$(RESET)\n%s\n' "$$out"; \
	  exit 1; \
	fi

# ── Benchmark ────────────────────────────────────────────────────────────────

.PHONY: bench

bench: build ## Recall benchmark against the pinned OSS corpus (needs network)
	$(GO) run ./bench

# ── Contract ─────────────────────────────────────────────────────────────────

# The scanner mirrors Lathe's rules by hand instead of importing them, so the
# mirror can drift. This gate scans a fixture service and hands the result to a
# lathe built from the latest main commit: specsync must load and stage the
# manifest, and codegen must emit commands from it. The fixture is copied to a
# temp dir outside this repository so its own git state and .gitignore cannot
# leak into the scan.
LATHE_PKG    := github.com/lathe-cli/lathe/cmd/lathe
CONTRACT_DIR := .local/contract

.PHONY: contract

contract: build ## Verify scan output against lathe@main (needs network)
	@rm -rf $(CONTRACT_DIR)
	@mkdir -p $(CONTRACT_DIR)/bin
	GOBIN=$(abspath $(CONTRACT_DIR)/bin) GOPROXY=direct $(GO) install $(LATHE_PKG)@main
	@svcroot=$$(mktemp -d) && \
	cp -R bench/contract/billing-svc "$$svcroot/billing-svc" && \
	cp -R bench/contract/app $(CONTRACT_DIR)/app && \
	./bin/lathe-scan "$$svcroot/billing-svc" --out $(CONTRACT_DIR)/app/specs; \
	status=$$?; rm -rf "$$svcroot"; exit $$status
	cd $(CONTRACT_DIR)/app && $(abspath $(CONTRACT_DIR)/bin/lathe) bootstrap
	@test -s $(CONTRACT_DIR)/app/internal/generated/billing_api/billing_api_gen.go || \
	  { printf 'contract: codegen emitted no billing_api module\n'; exit 1; }
	@printf '\n$(GREEN)  ✓ scan output holds against $(CYAN)lathe@main$(RESET)\n\n'

# ── Maintenance ──────────────────────────────────────────────────────────────

.PHONY: tidy clean

tidy: ## Tidy go.mod / go.sum
	$(GO) mod tidy

clean: ## Remove build artifacts
	rm -rf $(OUT_DIR)

# ── Help ─────────────────────────────────────────────────────────────────────

.PHONY: help

help: ## Show available targets
	@awk 'BEGIN {FS = ":.*## "; printf "\n$(BOLD)lathe-scan$(RESET) — API spec discovery for Lathe\n"} \
		/^# ── / {n = $$0; gsub(/(^# ── | ─+$$)/, "", n); printf "\n$(BOLD)%s$(RESET)\n", n} \
		/^[a-zA-Z_-]+:.*## / {printf "  $(CYAN)make %-12s$(RESET) %s\n", $$1, $$2} \
		END {printf "\n"}' $(MAKEFILE_LIST)
