.PHONY: build test race lint vet verify docker

build:
	go build -o bin/evidra-agentgateway ./cmd/bridge

test:
	go test ./... -count=1

race:
	go test -race ./... -count=1

lint:
	golangci-lint run

vet:
	go vet ./...

verify: lint vet test race

docker:
	docker build -t evidra-agentgateway:dev .
