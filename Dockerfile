# syntax=docker/dockerfile:1.24

FROM --platform=$BUILDPLATFORM golang:1.26.4@sha256:d184d9be4c13614e28498d632eeaaac704d662f18ad357e1df74a44424236cea AS build

ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
ARG COMMIT=none

WORKDIR /src

COPY go.mod go.sum ./
RUN --mount=type=cache,id=drydock-go-mod,target=/go/pkg/mod,sharing=locked \
  go mod download

COPY . .
RUN --mount=type=cache,id=drydock-go-mod,target=/go/pkg/mod,sharing=locked \
  --mount=type=cache,id=drydock-go-build-${TARGETOS}-${TARGETARCH},target=/root/.cache/go-build,sharing=locked \
  CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build \
  -trimpath \
  -ldflags="-s -w -X main.version=${VERSION} -X main.commit=${COMMIT}" \
  -o /drydock \
  ./cmd/drydock

FROM gcr.io/distroless/static:nonroot@sha256:963fa6c544fe5ce420f1f54fb88b6fb01479f054c8056d0f74cc2c6000df5240

ENV HOME=/tmp
ENV XDG_CACHE_HOME=/tmp/.cache
WORKDIR /workspace

LABEL org.opencontainers.image.title="drydock"
LABEL org.opencontainers.image.description="Inspect your Argo CD fleet without getting wet"
LABEL org.opencontainers.image.url="https://github.com/sholdee/drydock"
LABEL org.opencontainers.image.source="https://github.com/sholdee/drydock"
LABEL org.opencontainers.image.licenses="Apache-2.0"

COPY --from=build /drydock /drydock

USER 65532:65532
ENTRYPOINT ["/drydock"]
