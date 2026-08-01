# The API daemon. The console is a separate image built from ui/Dockerfile and
# served on its own host — see ui/Dockerfile and LLM.md.
#
# Every dependency resolves through the default module proxy and is checked
# against the default checksum database, so a tag that moves after the fact
# cannot change what this image contains: proxy.golang.org serves the zip it
# first recorded, and sum.golang.org is an append-only witness of that zip's
# hash. go.sum carries the witnessed hashes — see LLM.md, "Dependency pinning".
# Nothing here is fetched from the git host, so the build needs no credential.
FROM golang:1.26.4-alpine AS build
RUN apk add --no-cache git
ARG VERSION=dev

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath \
      -ldflags="-s -w -X main.version=${VERSION}" -o /app/amld ./cmd/amld/

# Third-party license and notice texts for the binary just built, derived from
# its module graph while the module cache is still present.
RUN CGO_ENABLED=0 go run ./internal/notice -pkg ./cmd/amld -name amld \
      -o /app/THIRD-PARTY-NOTICES

# Runtime
#
# The binary goes on PATH. It was at /app/amld, which alpine's PATH does not
# contain, so `amld` named anything only as an absolute path: the release smoke's
# `command -v amld` exited 127 and no v0.3.x image was ever published. On PATH the
# name works from an entrypoint, from `docker run <image> amld …`, and from a
# shell in the container — one name, everywhere.
FROM alpine:3.21
RUN apk add --no-cache ca-certificates tzdata
COPY --from=build /app/amld /usr/local/bin/amld
COPY --from=build /app/THIRD-PARTY-NOTICES /app/THIRD-PARTY-NOTICES
COPY LICENSE /app/LICENSE
WORKDIR /data
EXPOSE 8090
ENTRYPOINT ["amld"]
CMD ["serve", "--http=0.0.0.0:8090"]
