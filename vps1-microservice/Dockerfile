FROM golang:1.25-bookworm AS build
WORKDIR /workspace

COPY go.mod go.sum ./
RUN go mod download

COPY . .
ARG TARGETOS=linux
ARG TARGETARCH=amd64
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -trimpath -ldflags="-s -w" -o /out/vps1-microservice .

FROM debian:bookworm-slim
WORKDIR /app
RUN apt-get update && \
    apt-get install -y --no-install-recommends ca-certificates webp && \
    rm -rf /var/lib/apt/lists/*
COPY --from=build /out/vps1-microservice /app/vps1-microservice
EXPOSE 8080
ENTRYPOINT ["/app/vps1-microservice"]
