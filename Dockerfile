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

RUN apk add --no-cache ca-certificates
COPY --from=builder /bin/ferrogw /bin/ferrogw

EXPOSE 8080

ENTRYPOINT ["/bin/ferrogw"]
