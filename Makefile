.PHONY: help build test test-e2e run clean

help: ## Show this help message
	@grep -E '^[a-zA-Z0-9_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-15s\033[0m %s\n", $$1, $$2}'

build: ## Build container image (aqsh:latest and rophy/aqsh:<version>)
	./scripts/build.sh

test: ## Run Go tests
	go test ./...

test-e2e: ## Run integration tests with coverage
	./scripts/run-e2e-test.sh

run: ## Run aqsh locally in 'both' mode
	go run ./cmd/aqsh

clean: ## Remove built binary and coverage data
	rm -f aqsh
	rm -rf coverage/
