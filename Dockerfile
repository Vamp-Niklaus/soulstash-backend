# Build stage
FROM golang:1.27-alpine AS builder

WORKDIR /build

RUN apk add --no-cache git ca-certificates tzdata

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
    -ldflags="-w -s -extldflags '-static'" \
    -o /build/bin/server ./cmd/server

# Final runtime stage: minimal non-root image
FROM alpine:3.20

RUN apk add --no-cache ca-certificates tzdata && \
    addgroup -g 10001 -S appgroup && \
    adduser -u 10001 -S appuser -G appgroup

WORKDIR /app

COPY --from=builder /build/bin/server /app/server

USER appuser:appgroup

EXPOSE 3000

ENV PORT=3000 \
    APP_ENV=production

ENTRYPOINT ["/app/server"]
