GOBIN := $(shell go env GOPATH)/bin

.PHONY: build test lint generate install

build:
	go build ./...

test:
	go test ./...

lint:
	golangci-lint run

generate:
	$(GOBIN)/sqlc generate

install:
	go install ./cmd/tend
