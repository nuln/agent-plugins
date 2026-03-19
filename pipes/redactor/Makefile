.PHONY: all fmt lint test coverage tidy check clean

all: check fmt lint test

fmt:
	go fmt ./...

lint:
	golangci-lint run ./...

test:
	go test -v ./...

coverage:
	go test -v -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out -o coverage.html

tidy:
	go mod tidy
	go mod verify

check: tidy
	go vet ./...
	go mod tidy

clean:
	rm -f coverage.out coverage.html
	go clean -testcache
