.PHONY: build run test lint docker-build

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
