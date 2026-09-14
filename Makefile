.PHONY: all build test run clean fmt vet

BINARY := signet
VERSION := $(shell git describe --tags --always --dirty)
LDFLAGS := -ldflags "-X github.com/vulnetix/signet/internal/version.Version=$(VERSION)"

all: fmt vet test build

build:
	go build $(LDFLAGS) -o $(BINARY) ./cmd/signet

run:
	go run $(LDFLAGS) ./cmd/signet

test:
	go test -race ./...

clean:
	rm -f $(BINARY)

fmt:
	go fmt ./...

vet:
	go vet ./...
