SHELL := /bin/bash
.DEFAULT_GOAL := help
# Honor go.mod's pinned toolchain even on hosts configured with GOTOOLCHAIN=local.
export GOTOOLCHAIN := auto
export GOSUMDB := sum.golang.org

.PHONY: help setup infra down migrate dev api worker frontend build check test test-integration test-race test-e2e browsers

help:
	@echo 'make setup             Install dependencies, generate local secrets, start infrastructure, migrate'
	@echo 'make dev               Run API, legacy/Temporal workers, WhatsApp connector, and frontend'
	@echo 'make check             Go vet + frontend typecheck/lint + all Go/frontend unit tests'
	@echo 'make test-integration  Run Go tests with PostgreSQL and Redis enabled'
	@echo 'make test-race         Run Go integration tests with the race detector (requires C compiler)'
	@echo 'make browsers          Install Playwright Chromium'
	@echo 'make test-e2e          Run browser tests against an isolated backend and mock provider'
	@echo 'make build             Build API, worker, and production frontend'
	@echo 'make down              Stop project infrastructure, preserving PostgreSQL data'

setup:
	@bash scripts/setup.sh

infra:
	docker compose up -d --wait

down:
	docker compose down

migrate:
	@bash scripts/with-env.sh go run ./cmd/migrate

dev:
	@bash scripts/with-env.sh bash scripts/dev.sh

api:
	@bash scripts/with-env.sh go run ./cmd/api

worker:
	@bash scripts/with-env.sh go run ./cmd/worker

orchestrator:
	@bash scripts/with-env.sh go run ./cmd/orchestrator

connector:
	@bash scripts/with-env.sh go run ./cmd/connector

frontend:
	@bash scripts/with-env.sh npm --prefix frontend run dev

build:
	go build -o bin/api ./cmd/api
	go build -o bin/worker ./cmd/worker
	go build -o bin/orchestrator ./cmd/orchestrator
	go build -o bin/connector ./cmd/connector
	@bash scripts/with-env.sh npm --prefix frontend run build

check:
	go vet ./...
	npm --prefix frontend run typecheck
	npm --prefix frontend run lint
	$(MAKE) test

test:
	go test ./...
	npm --prefix frontend test

test-integration:
	@bash scripts/with-env.sh node scripts/test-services.mjs go test -p 1 -count=1 ./...

test-race:
	@bash scripts/with-env.sh node scripts/test-services.mjs go test -race -p 1 -count=1 ./...

browsers:
	cd frontend && npx playwright install chromium

test-e2e:
	@bash scripts/with-env.sh node scripts/test-services.mjs npm --prefix frontend run test:e2e
