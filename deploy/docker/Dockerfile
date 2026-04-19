# syntax=docker/dockerfile:1.6
#
# ddns reference image — multi-stage build.
#
# Build stage compiles a fully static, stripped, trimpath-ed binary with
# version metadata injected via -ldflags. The runtime stage is Google's
# distroless "static" base, which ships only CA certs, /etc/passwd, and
# tzdata — everything our CGO-free binary needs and nothing else.
#
# Build with version metadata:
#   docker build -f deploy/docker/Dockerfile \
#     --build-arg VERSION=$(git describe --tags --always --dirty) \
#     --build-arg COMMIT=$(git rev-parse --short HEAD) \
#     --build-arg BUILD_DATE=$(date -u +%Y-%m-%dT%H:%M:%SZ) \
#     -t ddns:local .

FROM golang:1.26-alpine AS build
WORKDIR /src

# Pre-fetch modules separately so layer caching still works when only Go
# sources change.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILD_DATE=unknown

RUN CGO_ENABLED=0 GOOS=linux \
    go build -trimpath \
      -ldflags "-s -w \
        -X github.com/rootwarp/ddns/internal/version.Version=${VERSION} \
        -X github.com/rootwarp/ddns/internal/version.Commit=${COMMIT} \
        -X github.com/rootwarp/ddns/internal/version.BuildDate=${BUILD_DATE}" \
      -o /out/ddns ./cmd/ddns

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/ddns /ddns
USER nonroot:nonroot
ENTRYPOINT ["/ddns"]
CMD ["run", "--config", "/etc/ddns/config.yaml"]
