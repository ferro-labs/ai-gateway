# Build stage
FROM golang:1.24-alpine AS builder

RUN apk add --no-cache git ca-certificates

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /bin/ferrogw ./cmd/ferrogw

# Runtime stage
FROM alpine:3.20

RUN apk add --no-cache ca-certificates && \
    addgroup -S ferro && adduser -S ferro -G ferro && \
    mkdir -p /app /data && chown ferro:ferro /app /data

COPY --from=builder --chown=ferro:ferro /bin/ferrogw /bin/ferrogw

WORKDIR /app

EXPOSE 8080

USER ferro

ENTRYPOINT ["/bin/ferrogw"]
