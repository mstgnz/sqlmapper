# Development tasks for sqlmapper.
#
# `make check` runs what CI runs, in the same order, so a red pipeline can be
# reproduced with one command.

BINARY      := sqlmapper
CMD         := ./cmd/sqlmapper
COVERAGE    := coverage.out
LINT_VERSION := v2.13.1

# Every package is kept above this. The README says so, and this is what
# enforces it.
COVERAGE_FLOOR := 85

# How long `make fuzz` looks for a crash. The seed corpus runs on every
# `make test`; this is for going further.
FUZZTIME ?= 60s

GO      ?= go
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo devel)
LDFLAGS := -s -w -X main.version=$(VERSION)

.DEFAULT_GOAL := help

.PHONY: help
help: ## List the available targets
	@grep -hE '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) \
		| awk 'BEGIN {FS = ":.*?## "} {printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2}'

## --- the CI pipeline ---------------------------------------------------------

.PHONY: check
check: fmt-check vet test-race cover examples lint ## Run everything CI runs

.PHONY: fmt
fmt: ## Format the source
	gofmt -w .

.PHONY: fmt-check
fmt-check: ## Fail if anything is not gofmt-clean
	@unformatted=$$(gofmt -l .); \
	if [ -n "$$unformatted" ]; then \
		echo "These files are not gofmt-clean:"; \
		echo "$$unformatted"; \
		exit 1; \
	fi

.PHONY: vet
vet: ## Run go vet
	$(GO) vet ./...

.PHONY: test
test: ## Run the tests
	$(GO) test ./...

.PHONY: test-race
test-race: ## Run the tests under the race detector
	$(GO) test -race ./...

.PHONY: cover
cover: ## Report coverage and fail if a package is below the floor
	@$(GO) test -coverprofile=$(COVERAGE) ./... > /dev/null
	@$(GO) tool cover -func=$(COVERAGE) | tail -1
	@$(GO) test -cover ./... 2>&1 \
		| awk '/^ok/ && /coverage: [0-9.]+% of statements/ { \
			for (i = 1; i <= NF; i++) if ($$i == "coverage:") pct = $$(i+1) + 0; \
			printf "  %-46s %5.1f%%\n", $$2, pct; \
			if (pct < $(COVERAGE_FLOOR)) { failed = 1; low = low "\n  " $$2 " " pct "%" } \
		} \
		/^[ \t]/ && /coverage:/ { printf "  %-46s no tests\n", $$1 } \
		END { if (failed) { printf "\nbelow the %d%% floor:%s\n", $(COVERAGE_FLOOR), low; exit 1 } }'

.PHONY: cover-html
cover-html: ## Open the coverage report in a browser
	$(GO) test -coverprofile=$(COVERAGE) ./... > /dev/null
	$(GO) tool cover -html=$(COVERAGE)

.PHONY: examples
examples: ## Run the sample conversions end to end
	$(GO) run ./examples

.PHONY: lint
lint: ## Run the linter, at the version CI pins
	@command -v golangci-lint > /dev/null || { \
		echo "golangci-lint is not installed. Install $(LINT_VERSION):"; \
		echo "  go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(LINT_VERSION)"; \
		exit 1; \
	}
	golangci-lint run ./...

## --- building ----------------------------------------------------------------

.PHONY: build
build: ## Build the command into ./bin
	$(GO) build -trimpath -ldflags="$(LDFLAGS)" -o bin/$(BINARY) $(CMD)

.PHONY: install
install: ## Install the command into GOPATH/bin
	$(GO) install -trimpath -ldflags="$(LDFLAGS)" $(CMD)

.PHONY: docker
docker: ## Build the image the release publishes
	docker build --build-arg VERSION=$(VERSION) -t $(BINARY):$(VERSION) .

.PHONY: tidy
tidy: ## Tidy the module and verify the checksums
	$(GO) mod tidy
	$(GO) mod verify

.PHONY: fuzz
fuzz: ## Look for input that makes a parser panic (FUZZTIME=2m to run longer)
	$(GO) test ./tests/integration/ -run FuzzParseNeverPanics \
		-fuzz FuzzParseNeverPanics -fuzztime $(FUZZTIME)

.PHONY: bench
bench: ## Run the benchmarks
	$(GO) test -run '^$$' -bench . -benchmem ./tests/benchmark/

.PHONY: clean
clean: ## Remove build and coverage artifacts
	rm -rf bin $(COVERAGE)
