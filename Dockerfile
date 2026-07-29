# Stage 1: Build UI (Vite + React + @hanzo/gui)
# pnpm must match pnpm-lock.yaml's lockfileVersion (9.0), and --frozen-lockfile
# is mandatory so lockfile drift fails the build instead of resolving fresh.
FROM node:22-alpine AS ui
RUN corepack enable && corepack prepare pnpm@9.15.9 --activate
WORKDIR /ui
COPY ui/package.json ui/pnpm-lock.yaml ./
RUN pnpm install --frozen-lockfile
COPY ui/ .
RUN pnpm build

# Stage 2: Build Go binary with embedded UI
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
COPY --from=ui /ui/dist ./ui/dist/
RUN CGO_ENABLED=0 go build -trimpath -tags embedui \
      -ldflags="-s -w -X main.version=${VERSION}" -o /app/amld ./cmd/amld/

# Third-party license and notice texts for the binary just built, derived from
# its module graph while the module cache is still present.
RUN CGO_ENABLED=0 go run ./internal/notice -tags embedui -pkg ./cmd/amld -name amld \
      -o /app/THIRD-PARTY-NOTICES

# Stage 3: Runtime
FROM alpine:3.21
RUN apk add --no-cache ca-certificates tzdata
COPY --from=build /app/amld /app/amld
COPY --from=build /app/THIRD-PARTY-NOTICES /app/THIRD-PARTY-NOTICES
COPY LICENSE /app/LICENSE
WORKDIR /data
EXPOSE 8090
ENTRYPOINT ["/app/amld"]
CMD ["serve", "--http=0.0.0.0:8090"]
