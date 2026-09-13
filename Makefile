.PHONY: build test test-race lint vet fmt tidy clean install

BINARY := laravel-lsp
MODULE  := github.com/akyrey/laravel-lsp

# Derived from the nearest tag so local builds report a real version; falls
# back to the source-tree placeholder outside a git checkout.
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo 0.0.0-dev)
LDFLAGS := -X main.version=$(VERSION)

build:
	go build -ldflags '$(LDFLAGS)' -o $(BINARY) ./cmd/laravel-lsp

test:
	go test ./... -count=1

test-race:
	go test -race ./... -count=1

lint:
	golangci-lint run ./...

vet:
	go vet ./...

fmt:
	gofmt -s -w .

tidy:
	go mod tidy && go mod verify

clean:
	rm -f $(BINARY)

install:
	go install -ldflags '$(LDFLAGS)' ./cmd/laravel-lsp
