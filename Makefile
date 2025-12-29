.PHONY: help build test run clean

help: ## Show this help message
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-15s\033[0m %s\n", $$1, $$2}'

build: ## Build container image (aqsh:latest and rophy/aqsh:<version>)
	./scripts/build.sh

test: ## Run Go tests
	go test ./...

run: ## Run aqsh locally in 'both' mode
	go run ./cmd/aqsh

clean: ## Remove built binary
	rm -f aqsh
