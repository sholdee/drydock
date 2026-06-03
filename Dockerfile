# syntax=docker/dockerfile:1.24

FROM --platform=$BUILDPLATFORM golang:1.26.4@sha256:b4f9f8a927c6e8f24da1b653f0c7ca86fd1677fe371b03f69fd416166b527268 AS build

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
