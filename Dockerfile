# syntax=docker/dockerfile:1.27

FROM --platform=$BUILDPLATFORM golang:1.27.1@sha256:e0174e51e81218523251d85d248a90d24c3d5e81543b4f07a5d66229397db190 AS build

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

FROM gcr.io/distroless/static:nonroot@sha256:f7f8f729987ad0fdf6b05eeeae94b26e6a0f613bdf46feea7fc40f7bd72953e6

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
