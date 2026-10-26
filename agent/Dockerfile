# Owl agent (linux/amd64 and linux/arm64).
# Build from the REPO ROOT:
#   docker build -f agent/Dockerfile -t ghcr.io/<owner>/owl-agent:dev .
ARG GO_VERSION=1.27
FROM --platform=$BUILDPLATFORM golang:${GO_VERSION} AS build
ARG TARGETOS TARGETARCH VERSION=dev
WORKDIR /src
COPY agent/go.mod agent/go.sum ./
RUN go mod download
COPY agent/cmd ./cmd
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/agent ./cmd/agent

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/agent /agent
USER nonroot:nonroot
ENTRYPOINT ["/agent"]
