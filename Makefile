# ==============================================================================
# raglibrarian — Iteration 0 (pkg/domain only)
# ==============================================================================

.DEFAULT_GOAL := help
SHELL         := /bin/bash
.SHELLFLAGS   := -eu -o pipefail -c

GO            := go
GOLANGCI_LINT := golangci-lint

# Reads module paths from go.work so every command covers all modules
MODULES       := $(shell $(GO) work edit -json | grep '"DiskPath"' | sed 's/.*"DiskPath": "\(.*\)".*/\1/')

# ==============================================================================
# Help
# ==============================================================================

.PHONY: help
help: ## Show available commands
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) \
		| awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-15s\033[0m %s\n", $$1, $$2}'

# ==============================================================================
# Code Quality
# ==============================================================================

.PHONY: fmt
fmt: ## Format all Go code across all workspace modules
	@for m in $(MODULES); do $(GO) fmt $$m/...; done

.PHONY: vet
vet: ## Run go vet across all workspace modules
	@for m in $(MODULES); do $(GO) vet $$m/...; done

.PHONY: lint
lint: ## Run golangci-lint across all workspace modules
	@which $(GOLANGCI_LINT) > /dev/null 2>&1 || \
		(echo "golangci-lint not found. Install: https://golangci-lint.run/welcome/install" && exit 1)
	@for m in $(MODULES); do $(GOLANGCI_LINT) run $$m/...; done

# ==============================================================================
# Test
# ==============================================================================

.PHONY: test
test: ## Run all tests with race detector across all workspace modules
	@for m in $(MODULES); do $(GO) test -race -count=1 $$m/...; done

# ==============================================================================
# Composite
# ==============================================================================

.PHONY: check
check: fmt vet lint test ## Run fmt, vet, lint, and test in order