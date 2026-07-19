# syntax=docker/dockerfile:1.7

ARG NODE_VERSION=24.18.0
ARG GO_VERSION=1.26.5
ARG ALPINE_VERSION=3.24.1
ARG COMPOSE_VERSION=v5.1.4

FROM node:${NODE_VERSION}-alpine3.24 AS front

WORKDIR /frontend

COPY ui/package.json ui/package-lock.json ./

RUN --mount=type=cache,target=/root/.npm \
    npm ci

COPY ui/ ./

RUN npm run build

FROM golang:${GO_VERSION}-alpine3.24 AS back

WORKDIR /build

RUN apk add --no-cache gcc musl-dev

COPY core/go.mod core/go.sum ./

RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY core/ ./

ARG VERSION=development
ARG COMMIT_INFO=unknown
ARG BUILD_DATE=1970-01-01T00:00:00Z
ARG BRANCH=unknown

RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    mkdir -p /out && \
    CGO_ENABLED=1 go build \
      -trimpath \
      -buildvcs=false \
      -ldflags "-s -w \
        -X github.com/RA341/dockman/internal/info.Version=${VERSION} \
        -X github.com/RA341/dockman/internal/info.CommitInfo=${COMMIT_INFO} \
        -X github.com/RA341/dockman/internal/info.BuildDate=${BUILD_DATE} \
        -X github.com/RA341/dockman/internal/info.Branch=${BRANCH}" \
      -o /out/dockman \
      ./cmd/docker

FROM alpine:${ALPINE_VERSION} AS compose_cli_downloader

WORKDIR /download

RUN apk --no-cache add curl ca-certificates

ARG COMPOSE_VERSION
ARG TARGETARCH

RUN case "${TARGETARCH}" in \
      amd64) compose_arch="x86_64" ;; \
      arm64) compose_arch="aarch64" ;; \
      *) echo "Unsupported architecture: ${TARGETARCH}" >&2; exit 1 ;; \
    esac && \
    curl --fail --show-error --location \
      "https://github.com/docker/compose/releases/download/${COMPOSE_VERSION}/docker-compose-linux-${compose_arch}" \
      --output "./docker-compose-linux-${compose_arch}" && \
    curl --fail --show-error --location \
      "https://github.com/docker/compose/releases/download/${COMPOSE_VERSION}/checksums.txt" \
      --output ./checksums.txt && \
    grep "docker-compose-linux-${compose_arch}$" ./checksums.txt | sha256sum -c - && \
    mv "./docker-compose-linux-${compose_arch}" ./docker-compose && \
    chmod +x ./docker-compose

FROM alpine:${ALPINE_VERSION} AS runtime

# docker-cli powers the run-a-docker-command feature on the local host
# (remote hosts run it over ssh); it also works through DOCKER_HOST/socketproxy.
RUN apk add --no-cache tzdata su-exec docker-cli

COPY pkg/docker/docker-entrypoint.sh /usr/local/bin/entrypoint.sh
RUN chmod +x /usr/local/bin/entrypoint.sh

COPY --from=compose_cli_downloader /download/docker-compose /usr/local/bin/docker-compose

# identify dockman containers
LABEL dockman.container=true

ARG VERSION=development
ARG COMMIT_INFO=unknown
ARG BUILD_DATE=1970-01-01T00:00:00Z
ARG BRANCH=unknown
ARG NODE_VERSION
ARG GO_VERSION
ARG ALPINE_VERSION
ARG COMPOSE_VERSION

LABEL org.opencontainers.image.title="Dockman" \
      org.opencontainers.image.description="Docker management UI built from the Dockman integration fork" \
      org.opencontainers.image.source="https://github.com/cerede2000/dockman" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${COMMIT_INFO}" \
      org.opencontainers.image.created="${BUILD_DATE}" \
      dev.dockman.build.branch="${BRANCH}" \
      dev.dockman.build.node="${NODE_VERSION}" \
      dev.dockman.build.go="${GO_VERSION}" \
      dev.dockman.build.alpine="${ALPINE_VERSION}" \
      dev.dockman.build.compose="${COMPOSE_VERSION}"

WORKDIR /app

COPY --from=back /out/dockman dockman

COPY --from=front /frontend/dist/ ./dist

RUN docker-compose version && docker --version

EXPOSE 8866

# set default envs so that entrypoint can handle the permissions
ENV DOCKMAN_UI_PATH=./dist
ENV DOCKMAN_COMPOSE_ROOT=/compose
ENV DOCKMAN_CONFIG=/config
ENV DOCKMAN_YAML_PATH=/config/dockyaml

ENTRYPOINT ["entrypoint.sh"]

CMD ["./dockman"]
