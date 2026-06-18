FROM golang:1.22-bookworm AS build
WORKDIR /workspace

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags="-s -w" -o /out/image-process-worker .

FROM debian:bookworm-slim
WORKDIR /app
RUN apt-get update && \
    apt-get install -y --no-install-recommends ca-certificates webp && \
    rm -rf /var/lib/apt/lists/*
COPY --from=build /out/image-process-worker /app/image-process-worker
EXPOSE 8080
ENTRYPOINT ["/app/image-process-worker"]
