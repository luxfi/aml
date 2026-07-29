.PHONY: build ui notice test vet run clean

VERSION := $(shell git describe --tags --always 2>/dev/null || echo dev)
LDFLAGS := -ldflags="-s -w -X main.version=$(VERSION)"
NOTICE  := THIRD-PARTY-NOTICES

# The product binary embeds the admin dashboard, so it always builds the UI
# first. Bare `go build`/`go vet`/`go test`/`go install` need no UI: without
# the embedui tag ui/embed.go supplies a placeholder tree.
build: ui notice
	CGO_ENABLED=0 go build -trimpath -tags embedui $(LDFLAGS) -o amld ./cmd/amld/

ui:
	pnpm --dir ui install --frozen-lockfile
	pnpm --dir ui build

# Third-party license and notice texts for the binary, derived from its module
# graph. Generated with the same tags and CGO setting the product ships with, so
# it describes the artifact that is distributed and not some other graph — which
# also means dist/ has to exist first for the embed directive to resolve. No
# -platforms: this target builds one binary, for this host.
notice: ui
	CGO_ENABLED=0 go run ./internal/notice -tags embedui -pkg ./cmd/amld -name amld -o $(NOTICE)

test:
	go test -race -count=1 ./...

vet:
	go vet ./...

run: build
	./amld serve --dev

clean:
	rm -f amld $(NOTICE)
	rm -rf data/ ui/dist/
