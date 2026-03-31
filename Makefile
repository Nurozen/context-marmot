MODULE := github.com/nurozen/context-marmot
BINARY := bin/marmot

.PHONY: build test lint clean fmt vet tidy

build:
	go build -o $(BINARY) ./cmd/marmot

test:
	go test -race -count=1 ./...

test-v:
	go test -race -count=1 -v ./...

test-cover:
	go test -race -count=1 -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out -o coverage.html

lint: vet
	@which golangci-lint > /dev/null 2>&1 || echo "golangci-lint not installed"
	@which golangci-lint > /dev/null 2>&1 && golangci-lint run ./... || true

vet:
	go vet ./...

fmt:
	gofmt -s -w .

tidy:
	go mod tidy

clean:
	rm -rf bin/ coverage.out coverage.html
