.PHONY: build run test lint docker-build compose-up compose-down migrate-create migrate-up migrate-down

build:
	go build -o bin/gateway ./cmd/gateway

run: build
	./bin/gateway

test:
	go test ./... -v -race

lint:
	golangci-lint run ./...

docker-build:
	docker build -f deployments/docker/Dockerfile -t llm-gateway:latest .

# Full local stack: gateway + Postgres + Redis + Prometheus + Grafana.
# Requires a .env file at the repo root with at least one provider key
# (see .env.example).
compose-up:
	docker compose -f deployments/docker/docker-compose.yml up -d --build

# Tears down the stack AND its volumes for a clean reset. Run plain
# `docker compose down` if you want to keep Postgres / Grafana state.
compose-down:
	docker compose -f deployments/docker/docker-compose.yml down -v

# Scaffold a new pair of migration files. Requires the migrate CLI:
#   go install -tags 'postgres' github.com/golang-migrate/migrate/v4/cmd/migrate@latest
# Usage: make migrate-create NAME=add_widgets_table
migrate-create:
	@test -n "$(NAME)" || (echo "Usage: make migrate-create NAME=description_here"; exit 1)
	migrate create -ext sql -dir migrations -seq $(NAME)

# Dev-time helpers: in the compose stack the gateway runs migrations
# on startup, so these are only for running migrations against an
# already-provisioned database from your shell. Reads the DSN from
# the GATEWAY_DATABASE__POSTGRES__DSN env var.
migrate-up:
	migrate -path migrations -database "$$GATEWAY_DATABASE__POSTGRES__DSN" up

migrate-down:
	migrate -path migrations -database "$$GATEWAY_DATABASE__POSTGRES__DSN" down 1
