# Stage 1: Build UI (Vite + React + @hanzo/gui)
FROM node:22-alpine AS ui
RUN corepack enable && corepack prepare pnpm@8.15.9 --activate
WORKDIR /ui
COPY ui/package.json ui/pnpm-lock.yaml* ./
RUN pnpm install --frozen-lockfile 2>/dev/null || pnpm install --no-frozen-lockfile
COPY ui/ .
RUN pnpm build

# Stage 2: Build Go binary with embedded UI
FROM golang:1.26.4-alpine AS build
RUN apk add --no-cache gcc musl-dev git

ENV GOPRIVATE=github.com/luxfi/*,github.com/hanzoai/*
ENV GONOSUMDB=github.com/luxfi/*,github.com/hanzoai/*
ENV GOPROXY=direct

ARG GITHUB_TOKEN
RUN if [ -n "$GITHUB_TOKEN" ]; then \
      git config --global url."https://x-access-token:${GITHUB_TOKEN}@github.com/".insteadOf "https://github.com/"; \
    fi

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=ui /ui/dist ./ui/dist/
RUN CGO_ENABLED=1 go build -ldflags="-s -w -X main.version=$(git describe --tags --always 2>/dev/null || echo dev)" -o /app/amld ./cmd/amld/

# Stage 3: Runtime
FROM alpine:3.21
RUN apk add --no-cache ca-certificates tzdata
COPY --from=build /app/amld /app/amld
WORKDIR /data
EXPOSE 8090
ENTRYPOINT ["/app/amld"]
CMD ["serve", "--http=0.0.0.0:8090"]
