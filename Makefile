ADDR ?= :8080

.PHONY: help run test test-integration cover vet fmt lint build docker tidy

help: ## show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN{FS=":.*?## "}{printf "  %-16s %s\n", $$1, $$2}'

run: ## run the relay locally (ADDR=:8080)
	go run . --addr $(ADDR)

test: ## unit tests with the race detector
	go test -race ./...

test-integration: ## integration tests (full HTTP round-trip)
	go test -race -tags integration -run RoundTrip ./...

cover: ## unit test coverage summary
	go test -race -coverprofile=coverage.out ./... && go tool cover -func=coverage.out | tail -1

vet: ## go vet
	go vet ./...

fmt: ## fail if any file needs gofmt
	@out="$$(gofmt -l .)"; test -z "$$out" || { echo "gofmt needed:"; echo "$$out"; exit 1; }

lint: vet fmt ## vet + fmt check

build: ## build the static binary
	CGO_ENABLED=0 go build -o chat-relay .

docker: ## build the container image
	docker build -t chat-relay:local .

tidy: ## tidy go.mod
	go mod tidy
