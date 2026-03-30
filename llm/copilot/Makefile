.PHONY: fmt lint test build

fmt:
	gofmt -w .

lint:
	golangci-lint run ./...

test:
	go test -v -race ./...

build:
	go build ./...

all: fmt lint test build
