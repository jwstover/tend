GOBIN := $(shell go env GOPATH)/bin

.PHONY: build test generate install

build:
	go build ./...

test:
	go test ./...

generate:
	$(GOBIN)/sqlc generate

install:
	go install ./cmd/tend
