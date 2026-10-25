GO_VERSION := $(shell awk '$$1 == "go" { print $$2 }' go.mod)
COMPOSE := docker compose --env-file .env -f deployments/compose.yaml

.DEFAULT_GOAL := run
.PHONY: run docker-up docker-stop docker-logs status db-up db-migrate db-validate db-info db-down test-integration

run:
	go run ./cmd/giveawaysbot

test-integration:
	go test -tags=integration -race -count=1 -timeout=10m ./tests/...

status:
	$(COMPOSE) ps -a

db-up:
	$(COMPOSE) up -d --wait postgres

db-migrate:
	$(COMPOSE) run --rm flyway migrate

db-validate:
	$(COMPOSE) run --rm flyway validate

db-info:
	$(COMPOSE) run --rm flyway info

db-down:
	$(COMPOSE) stop postgres

docker-up:
	GO_VERSION=$(GO_VERSION) $(COMPOSE) up -d --build bot

docker-stop:
	$(COMPOSE) stop bot

docker-logs:
	$(COMPOSE) logs -f bot
