# Compatible with GNU Make 3.81 (macOS) and 4.x (WSL2).
SHELL := /bin/sh
.DEFAULT_GOAL := help

-include .env
export

CONTRACTS_TAG := v0.1.0
CONTRACTS_URL := https://github.com/tahananazkaban/ngumbah-contracts.git\#tag=$(CONTRACTS_TAG)
SQLC_IMAGE := sqlc/sqlc:1.31.1
LINT_IMAGE := golangci/golangci-lint:v2.15.0

.PHONY: help env build run test lint generate generate-check migrate seed docker-build

help: ## List available commands
	@grep -E '^[a-zA-Z_-]+:.*## ' $(firstword $(MAKEFILE_LIST)) | \
		awk 'BEGIN {FS = ":.*## "}; {printf "  %-15s %s\n", $$1, $$2}'

env: ## Create .env from .env.example if missing
	@[ -f .env ] || { cp .env.example .env && echo "created .env"; }

build: ## Build the binary into bin/
	@go build -o bin/tenant-svc ./cmd/tenant-svc

run: env ## Run with live reload (needs make infra-up in ngumbah-platform)
	@go tool air

test: env ## Run tests against tenant_db (needs make infra-up in ngumbah-platform)
	@go test ./... -count=1

lint: ## Run golangci-lint in Docker
	@docker run --rm -v "$(CURDIR):/src" -w /src $(LINT_IMAGE) golangci-lint run ./...

generate: ## Regenerate gRPC code (contract $(CONTRACTS_TAG)) and sqlc code
	@rm -rf gen && buf generate "$(CONTRACTS_URL)"
	@docker run --rm -v "$(CURDIR):/src" -w /src $(SQLC_IMAGE) generate

generate-check: ## Fail if committed generated code is out of date
	@sh scripts/generate-check.sh

migrate: env ## Apply database migrations
	@go run ./cmd/tenant-svc migrate

seed: env ## Create or refresh the demo tenant
	@go run ./cmd/tenant-svc seed

docker-build: ## Build the container image
	@docker build -t ngumbah-tenant-svc:dev .
