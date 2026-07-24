GO          ?= go
OUT_DIR     := ./bin
BINDIR      ?= $(if $(GOBIN),$(GOBIN),$(or $(GOPATH),$(HOME)/go)/bin)
BIN         := lathe-scan

BOLD  := \033[1m
CYAN  := \033[36m
GREEN := \033[32m
RESET := \033[0m

.DEFAULT_GOAL := help

# ── Build ────────────────────────────────────────────────────────────────────

.PHONY: build install

build: ## Build local lathe-scan binary into ./bin/lathe-scan
	@mkdir -p $(OUT_DIR)
	$(GO) build -trimpath -o $(OUT_DIR)/$(BIN) .
	@printf '\n$(GREEN)  ✓ built $(CYAN)$(OUT_DIR)/$(BIN)$(RESET)\n\n'

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
	@out=$$(gofmt -l main.go internal); \
	if [ -n "$$out" ]; then \
	  printf '$(BOLD)gofmt violations:$(RESET)\n%s\n' "$$out"; \
	  exit 1; \
	fi

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
