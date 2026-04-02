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
	@if [ -f go.work ]; then \
		echo "go.work exists, skipping go mod tidy"; \
	else \
		go mod tidy; \
		go mod verify; \
	fi

check: tidy
	go vet ./...

clean:
	rm -f coverage.out coverage.html
	go clean -testcache
