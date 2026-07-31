.PHONY: build ui notice test vet run clean

VERSION := $(shell git describe --tags --always 2>/dev/null || echo dev)
LDFLAGS := -ldflags="-s -w -X main.version=$(VERSION)"
NOTICE  := THIRD-PARTY-NOTICES

# The daemon is the API. The console is a separate artifact with its own image
# (ui/Dockerfile), so nothing here waits on a bundle.
build: notice
	CGO_ENABLED=0 go build -trimpath $(LDFLAGS) -o amld ./cmd/amld/

# The console, for a local look. The image build runs the same two commands.
ui:
	npm --prefix ui ci
	npm --prefix ui run build

# Third-party license and notice texts for the binary, derived from its module
# graph. Generated with the same tags and CGO setting the product ships with, so
# it describes the artifact that is distributed and not some other graph. No
# -platforms: this target builds one binary, for this host.
notice:
	CGO_ENABLED=0 go run ./internal/notice -pkg ./cmd/amld -name amld -o $(NOTICE)

test:
	go test -race -count=1 ./...

vet:
	go vet ./...

run: build
	./amld serve --dev

clean:
	rm -f amld $(NOTICE)
	rm -rf data/ ui/dist/
