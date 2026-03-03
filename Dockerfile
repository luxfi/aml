FROM golang:1.26-alpine AS build
RUN apk add --no-cache gcc musl-dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=1 go build -ldflags="-s -w -X main.version=$(git describe --tags --always 2>/dev/null || echo dev)" -o /app/amld ./cmd/amld/

FROM alpine:3.21
RUN apk add --no-cache ca-certificates tzdata
COPY --from=build /app/amld /app/amld
WORKDIR /data
EXPOSE 8090
ENTRYPOINT ["/app/amld"]
CMD ["serve", "--http=0.0.0.0:8090"]
