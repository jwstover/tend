GOBIN := $(shell go env GOPATH)/bin

.PHONY: build test lint generate install snapshot release-check

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

snapshot:
	goreleaser release --snapshot --clean

release-check:
	goreleaser check
