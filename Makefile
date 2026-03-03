.PHONY: build test run clean

VERSION := $(shell git describe --tags --always 2>/dev/null || echo dev)
LDFLAGS := -ldflags="-s -w -X main.version=$(VERSION)"

build:
	CGO_ENABLED=1 go build $(LDFLAGS) -o amld ./cmd/amld/

test:
	go test -race -count=1 ./...

run: build
	./amld serve --dev

clean:
	rm -f amld
	rm -rf data/
