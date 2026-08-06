# Tiny OpenAI-compatible mock upstream for the fullstack observability demo.
FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY main.go ./
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /mockllm .

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /mockllm /mockllm
EXPOSE 9090
ENTRYPOINT ["/mockllm"]
