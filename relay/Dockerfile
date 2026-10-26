# Owl relay + web app in one image (linux/amd64 and linux/arm64).
# Build from the REPO ROOT:
#   docker build -f relay/Dockerfile -t ghcr.io/<owner>/owl-relay:dev .

# Used in a FROM line, so it must be declared before the first FROM (global scope).
ARG GO_VERSION=1.27

# 1. Web app: static files, same for every architecture, so build once on the build machine.
FROM --platform=$BUILDPLATFORM node:22-alpine AS web
WORKDIR /web
COPY web/package.json web/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY web/ ./
RUN npm run build

# 2. Relay: Go cross-compiles for the target architecture (no emulation).
FROM --platform=$BUILDPLATFORM golang:${GO_VERSION} AS relay
ARG TARGETOS TARGETARCH VERSION=dev
WORKDIR /src
COPY relay/go.mod ./
COPY relay/cmd ./cmd
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/relay ./cmd/relay

# 3. Runtime: distroless, non-root. Serves the API and the web app.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=relay /out/relay /relay
COPY --from=web /web/dist /web
ENV RELAY_WEB_DIR=/web
USER 65532:65532
EXPOSE 2300 8443
ENTRYPOINT ["/relay"]
