MODULE := github.com/nurozen/context-marmot
BINARY := bin/marmot
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.date=$(DATE)

.PHONY: build build-eval build-ui build-full dev-ui test lint clean fmt vet tidy eval benchmark

build:
	go build -ldflags "$(LDFLAGS)" -o $(BINARY) ./cmd/marmot

build-eval:
	go build -o bin/marmot-eval ./cmd/marmot-eval

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

benchmark:
	go test -race -tags integration -count=1 -v -run TestSWEQABenchmark ./internal/

eval: build build-eval
	bin/marmot-eval --output testdata/eval/results

eval-dry: build build-eval
	bin/marmot-eval --questions 2 --output testdata/eval/results

build-ui:
	cd web && npm install && npm run build

build-full: build-ui build

dev-ui:
	cd web && npm run dev

clean:
	rm -rf bin/ coverage.out coverage.html
