MODULES := $(shell find . -name go.mod -not -path './.git/*' -exec dirname {} \; | sort)

GOLANGCI_LINT_VERSION ?= v2.14.0
GOVULNCHECK_VERSION   ?= v1.9.0

# Repo-local so tools are built with this repo's Go; golangci-lint refuses newer-Go modules otherwise.
TOOLS_BIN     := $(CURDIR)/bin
GOLANGCI_LINT := $(TOOLS_BIN)/golangci-lint
GOVULNCHECK   := $(TOOLS_BIN)/govulncheck

.DEFAULT_GOAL := check

.PHONY: help
help: ## Show available targets
	@grep -E '^[a-zA-Z_-]+:.*## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*## "}; {printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2}'

.PHONY: tools
tools: ## Install pinned dev tools into ./bin
	GOBIN=$(TOOLS_BIN) go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)
	GOBIN=$(TOOLS_BIN) go install golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION)

.PHONY: fmt
fmt: ## Format code in every module
	@for m in $(MODULES); do (cd $$m && $(GOLANGCI_LINT) fmt ./...) || exit 1; done

.PHONY: fmt-check
fmt-check: ## Fail if any file needs formatting
	@out=$$(gofmt -l $$(git ls-files --cached --others --exclude-standard '*.go')); \
	if [ -n "$$out" ]; then echo "needs gofmt:"; echo "$$out"; exit 1; fi

.PHONY: vet
vet: ## go vet every module
	@for m in $(MODULES); do echo "==> vet $$m"; (cd $$m && go vet ./...) || exit 1; done

.PHONY: lint
lint: ## golangci-lint every module
	@for m in $(MODULES); do echo "==> lint $$m"; (cd $$m && $(GOLANGCI_LINT) run ./...) || exit 1; done

.PHONY: tidy-check
tidy-check: ## Fail if go.mod/go.sum are not tidy in any module
	@for m in $(MODULES); do (cd $$m && go mod tidy -diff) || { echo "go mod tidy needed in $$m"; exit 1; }; done

.PHONY: test
test: ## Test the root module with the race detector
	go test -race -count=1 ./...

.PHONY: test-all
test-all: ## Test every module with the race detector
	@for m in $(MODULES); do echo "==> test $$m"; (cd $$m && go test -race -count=1 ./...) || exit 1; done

.PHONY: test-integration
test-integration: ## Run integration tests against real services (needs credentials in env)
	@for m in $(MODULES); do echo "==> integration $$m"; (cd $$m && go test -race -count=1 -tags integration ./...) || exit 1; done

.PHONY: cover
cover: ## Root module coverage report (coverage.out + summary)
	go test -race -count=1 -covermode=atomic -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out | tail -1

.PHONY: vuln
vuln: ## govulncheck every module
	@for m in $(MODULES); do echo "==> vuln $$m"; (cd $$m && $(GOVULNCHECK) ./...) || exit 1; done

.PHONY: check
check: fmt-check tidy-check vet lint test-all ## Everything CI runs, except vuln
