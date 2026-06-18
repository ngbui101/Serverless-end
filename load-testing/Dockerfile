FROM node:22-alpine

# Install k6 directly from GitHub releases (arch-aware)
RUN apk add --no-cache curl tar && \
    ARCH=$(uname -m) && \
    if [ "$ARCH" = "x86_64" ]; then K6_ARCH="amd64"; \
    elif [ "$ARCH" = "aarch64" ]; then K6_ARCH="arm64"; \
    else echo "Unsupported arch: $ARCH" && exit 1; fi && \
    curl -fL "https://github.com/grafana/k6/releases/download/v0.56.0/k6-v0.56.0-linux-${K6_ARCH}.tar.gz" \
        | tar xz -C /usr/local/bin --strip-components=1

WORKDIR /app
COPY server.js .
COPY public/ ./public/

EXPOSE 3000
CMD ["node", "server.js"]
